package services

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"determined/src/models"
)

// ChatSessionLocator resolves the verified live session used by chat.
type ChatSessionLocator interface {
	Locate() (models.SessionLink, error)
}

// ChatClient is the CLI-facing request/reply and event stream service.
type ChatClient struct {
	locator   ChatSessionLocator
	connector models.ChatConnector
	input     io.Reader
	output    io.Writer
	timeout   time.Duration
}

// NewChatClient wires session discovery, transport, and terminal streams.
func NewChatClient(locator ChatSessionLocator, connector models.ChatConnector, input io.Reader, output io.Writer, timeout time.Duration) *ChatClient {
	return &ChatClient{locator: locator, connector: connector, input: input, output: output, timeout: timeout}
}

// Ask sends one question and waits for its correlated reply.
func (c *ChatClient) Ask(ctx context.Context, question string) error {
	connection, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer connection.Close() //nolint:errcheck
	if err := connection.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		return fmt.Errorf("set chat deadline: %w", err)
	}
	request := models.ChatRequest{ID: "1", Type: models.ChatRequestMessage, Text: question}
	if err := writeChatRequest(connection, request); err != nil {
		return err
	}
	response, err := readChatResponse(connection)
	if err != nil {
		return fmt.Errorf("wait for chat reply: %w", err)
	}
	if response.ID != request.ID || response.Type != models.ChatResponseReply {
		return fmt.Errorf("unexpected chat response for request %q", request.ID)
	}
	fmt.Fprintln(c.output, response.Text)
	return nil
}

// Converse subscribes to events and exchanges stdin lines until either side
// closes. A server close is a clean end to the interactive session.
func (c *ChatClient) Converse(ctx context.Context) error {
	connection, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer connection.Close() //nolint:errcheck
	if err := writeChatRequest(connection, models.ChatRequest{ID: "subscribe", Type: models.ChatRequestSubscribe}); err != nil {
		return err
	}
	fmt.Fprintln(c.output, "determined chat — ask about status, plan, progress, activity, or logs")
	responses := make(chan models.ChatResponse, 16)
	reads := make(chan error, 1)
	writes := make(chan error, 1)
	sent := make(chan string)
	go c.readResponses(connection, responses, reads)
	go c.writeQuestions(connection, sent, writes)
	return c.conversationLoop(ctx, responses, reads, sent, writes)
}

func (c *ChatClient) conversationLoop(ctx context.Context, responses <-chan models.ChatResponse, reads <-chan error, sent <-chan string, writes <-chan error) error {
	pending := 0
	inputDone := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-reads:
			if err != nil {
				return err
			}
			fmt.Fprintln(c.output, "determined: chat session closed")
			return nil
		case <-sent:
			pending++
		case err := <-writes:
			if err != nil {
				return err
			}
			inputDone = true
		case response := <-responses:
			c.printResponse(response)
			pending = remainingReplies(pending, response)
		}
		if inputDone && pending == 0 {
			return nil
		}
	}
}

func remainingReplies(pending int, response models.ChatResponse) int {
	if response.ID != "" && pending > 0 {
		return pending - 1
	}
	return pending
}

func (c *ChatClient) connect(ctx context.Context) (models.ChatConnection, error) {
	link, err := c.locator.Locate()
	if err != nil {
		return nil, err
	}
	address := models.ChatURL(fmt.Sprintf("ws://localhost:%d/chat", link.Port))
	connection, err := c.connector.Connect(ctx, address)
	if err != nil {
		return nil, fmt.Errorf("connect to chat: %w", err)
	}
	return connection, nil
}

func writeChatRequest(connection models.ChatConnection, request models.ChatRequest) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode chat request: %w", err)
	}
	if err := connection.WriteText(payload); err != nil {
		return fmt.Errorf("send chat request: %w", err)
	}
	return nil
}

func readChatResponse(connection models.ChatConnection) (models.ChatResponse, error) {
	payload, err := connection.ReadText()
	if err != nil {
		return models.ChatResponse{}, err
	}
	var response models.ChatResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return models.ChatResponse{}, fmt.Errorf("decode chat response: %w", err)
	}
	return response, nil
}

func (c *ChatClient) readResponses(connection models.ChatConnection, responses chan<- models.ChatResponse, result chan<- error) {
	for {
		response, err := readChatResponse(connection)
		if err != nil {
			if connection.CleanClose(err) {
				result <- nil
			} else {
				result <- fmt.Errorf("read chat response: %w", err)
			}
			return
		}
		responses <- response
	}
}

func (c *ChatClient) printResponse(response models.ChatResponse) {
	switch response.Type {
	case models.ChatResponseEvent:
		fmt.Fprintf(c.output, "[event:%s] %s\n", response.Event, response.Text)
	case models.ChatResponseError:
		fmt.Fprintf(c.output, "[error] %s\n", response.Error)
	default:
		fmt.Fprintln(c.output, response.Text)
	}
}

func (c *ChatClient) writeQuestions(connection models.ChatConnection, sent chan<- string, result chan<- error) {
	scanner := bufio.NewScanner(c.input)
	for id := 1; scanner.Scan(); id++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		request := models.ChatRequest{ID: fmt.Sprint(id), Type: models.ChatRequestMessage, Text: text}
		sent <- request.ID
		if err := writeChatRequest(connection, request); err != nil {
			result <- err
			return
		}
	}
	result <- scanner.Err()
}
