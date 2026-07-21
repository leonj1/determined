package tests

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"determined/src/clients"
)

func TestWebSocketAcceptKeyMatchesRFCExample(t *testing.T) {
	got := clients.WebSocketAcceptKey("dGhlIHNhbXBsZSBub25jZQ==")
	if got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("accept key = %q, want RFC 6455 example", got)
	}
}

func rawWebSocket(t *testing.T, serverURL string) (net.Conn, *bufio.Reader) {
	t.Helper()
	target, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	connection, err := net.DialTimeout("tcp", target.Host, 2*time.Second)
	if err != nil {
		t.Fatalf("dial raw websocket: %v", err)
	}
	request := fmt.Sprintf("GET /chat HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n", target.Host)
	if _, err := io.WriteString(connection, request); err != nil {
		t.Fatalf("write raw handshake: %v", err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("read raw handshake: %v", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols || response.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("handshake = %s headers=%v", response.Status, response.Header)
	}
	connection.SetDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	return connection, reader
}

func writeMaskedFrame(t *testing.T, connection net.Conn, first byte, payload []byte) {
	t.Helper()
	mask := []byte{0x37, 0xfa, 0x21, 0x3d}
	frame := []byte{first, 0x80 | byte(len(payload))}
	frame = append(frame, mask...)
	for i, value := range payload {
		frame = append(frame, value^mask[i%4])
	}
	if _, err := connection.Write(frame); err != nil {
		t.Fatalf("write masked frame: %v", err)
	}
}

func readRawFrame(t *testing.T, reader *bufio.Reader) (byte, []byte) {
	t.Helper()
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		t.Fatalf("read raw frame header: %v", err)
	}
	length := int(header[1] & 0x7f)
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		t.Fatalf("read raw frame payload: %v", err)
	}
	return header[0] & 0x0f, payload
}

func TestWebSocketAnswersPingWithPong(t *testing.T) {
	serverURL, _, stop := startChatServer(t)
	defer stop()
	connection, reader := rawWebSocket(t, serverURL)
	defer connection.Close()

	writeMaskedFrame(t, connection, 0x89, []byte("alive"))
	opcode, payload := readRawFrame(t, reader)
	if opcode != 0x0a || string(payload) != "alive" {
		t.Fatalf("frame opcode=%d payload=%q, want matching pong", opcode, payload)
	}
}

func TestWebSocketRejectsUnsupportedFrames(t *testing.T) {
	for name, first := range map[string]byte{"binary": 0x82, "fragmented": 0x01} {
		t.Run(name, func(t *testing.T) {
			serverURL, _, stop := startChatServer(t)
			defer stop()
			connection, reader := rawWebSocket(t, serverURL)
			defer connection.Close()
			writeMaskedFrame(t, connection, first, nil)
			opcode, payload := readRawFrame(t, reader)
			if opcode != 0x08 || len(payload) < 2 || binary.BigEndian.Uint16(payload[:2]) != 1003 {
				t.Fatalf("close frame opcode=%d payload=%v, want code 1003", opcode, payload)
			}
		})
	}
}

func TestWebSocketRejectsOversizedFrameBeforeReadingPayload(t *testing.T) {
	serverURL, _, stop := startChatServer(t)
	defer stop()
	connection, reader := rawWebSocket(t, serverURL)
	defer connection.Close()
	header := []byte{0x81, 0xff, 0, 0, 0, 0, 0, 0x10, 0, 0x01}
	if _, err := connection.Write(header); err != nil {
		t.Fatalf("write oversized header: %v", err)
	}
	opcode, payload := readRawFrame(t, reader)
	if opcode != 0x08 || len(payload) < 2 || binary.BigEndian.Uint16(payload[:2]) != 1009 {
		t.Fatalf("close frame opcode=%d payload=%v, want code 1009", opcode, payload)
	}
}
