package tests

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"determined/src/clients"
	"determined/src/models"
	"determined/src/services"
)

type fixedChatLocator struct {
	link models.SessionLink
	err  error
}

func (l fixedChatLocator) Locate() (models.SessionLink, error) { return l.link, l.err }

func locatorForURL(t *testing.T, serverURL string) fixedChatLocator {
	t.Helper()
	target, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	port, err := strconv.Atoi(target.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	return fixedChatLocator{link: models.SessionLink{URL: serverURL, PID: 1, Port: port}}
}

func TestChatClientOneShotPrintsTheCorrelatedReply(t *testing.T) {
	serverURL, _, stop := startChatServer(t)
	defer stop()
	var output bytes.Buffer
	client := services.NewChatClient(locatorForURL(t, serverURL), clients.NewWebSocketDialer(), strings.NewReader(""), &output, time.Second)

	if err := client.Ask(context.Background(), "status"); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !strings.Contains(output.String(), "execution running") || !strings.Contains(output.String(), "2 of 3") {
		t.Fatalf("output = %q, want status prose", output.String())
	}
}

func TestChatClientInteractivePrintsRepliesBeforeEOFClose(t *testing.T) {
	serverURL, _, stop := startChatServer(t)
	defer stop()
	var output bytes.Buffer
	input := strings.NewReader("progress\nshow the plan\n")
	client := services.NewChatClient(locatorForURL(t, serverURL), clients.NewWebSocketDialer(), input, &output, time.Second)

	if err := client.Converse(context.Background()); err != nil {
		t.Fatalf("converse: %v", err)
	}
	text := output.String()
	if !strings.Contains(text, "2 of 3 steps") || !strings.Contains(text, "ship agent chat") {
		t.Fatalf("interactive output = %q, want both replies", text)
	}
}

func TestChatClientReportsMissingSession(t *testing.T) {
	client := services.NewChatClient(fixedChatLocator{err: services.ErrNoSession}, clients.NewWebSocketDialer(), strings.NewReader(""), &bytes.Buffer{}, time.Second)
	if err := client.Ask(context.Background(), "status"); err != services.ErrNoSession {
		t.Fatalf("error = %v, want ErrNoSession", err)
	}
}

type silentChatResponder struct{}

func (silentChatResponder) Answer(request models.ChatRequest) models.ChatResponse {
	time.Sleep(200 * time.Millisecond)
	return models.ChatResponse{ID: request.ID, Type: models.ChatResponseReply, Text: "late"}
}

func (silentChatResponder) Events(models.PlanSessionStatus, models.PlanSessionStatus) []models.ChatResponse {
	return nil
}

func TestChatClientOneShotTimesOutWithoutReply(t *testing.T) {
	source := newChatStatusSource(chatSnapshot())
	server := clients.NewPlanStatusServer(source, &fakeAnnotationSink{}, &fakeImplementSink{}, serverClock{}).WithChatResponder(silentChatResponder{})
	if err := server.Start(); err != nil {
		t.Fatalf("start silent server: %v", err)
	}
	defer shutdown(t, server)
	client := services.NewChatClient(locatorForURL(t, server.URL()), clients.NewWebSocketDialer(), strings.NewReader(""), &bytes.Buffer{}, 20*time.Millisecond)

	if err := client.Ask(context.Background(), "status"); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("error = %v, want reply timeout", err)
	}
}

func TestInteractiveChatTreatsServerShutdownAsCleanClose(t *testing.T) {
	source := newChatStatusSource(chatSnapshot())
	clock := serverClock{t: time.Now()}
	server := clients.NewPlanStatusServer(source, &fakeAnnotationSink{}, &fakeImplementSink{}, clock).
		WithChatResponder(services.NewChatService(source, clock))
	if err := server.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	input, inputWriter := io.Pipe()
	defer input.Close()
	defer inputWriter.Close()
	client := services.NewChatClient(locatorForURL(t, server.URL()), clients.NewWebSocketDialer(), input, &bytes.Buffer{}, time.Second)
	result := make(chan error, 1)
	go func() { result <- client.Converse(context.Background()) }()
	select {
	case <-source.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("client did not subscribe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown server: %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("converse returned %v, want clean close", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interactive client did not exit after server shutdown")
	}
}
