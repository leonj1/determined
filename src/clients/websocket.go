package clients

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // SHA-1 is mandated by RFC 6455 for the handshake
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"determined/src/models"
)

const (
	webSocketGUID        = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	webSocketMaxFrame    = 1 << 20
	webSocketText        = 0x1
	webSocketClose       = 0x8
	webSocketPing        = 0x9
	webSocketPong        = 0xa
	webSocketNormal      = 1000
	webSocketProtocol    = 1002
	webSocketUnsupported = 1003
	webSocketTooLarge    = 1009
)

// WebSocketCloseError describes a protocol close received or emitted while
// reading a frame.
type WebSocketCloseError struct {
	Code int
}

func (e WebSocketCloseError) Error() string {
	return fmt.Sprintf("websocket closed with code %d", e.Code)
}

// WebSocketConn is the minimal RFC 6455 text transport used by chat.
type WebSocketConn struct {
	conn       net.Conn
	reader     *bufio.Reader
	maskWrites bool
	expectMask bool
	writeMu    sync.Mutex
	closeOnce  sync.Once
}

// WebSocketDialer opens client-side WebSocket connections.
type WebSocketDialer struct{}

// NewWebSocketDialer constructs the production chat transport connector.
func NewWebSocketDialer() WebSocketDialer { return WebSocketDialer{} }

// Connect implements the services chat connector boundary.
func (WebSocketDialer) Connect(ctx context.Context, address models.ChatURL) (models.ChatConnection, error) {
	return DialWebSocket(ctx, string(address))
}

// WebSocketAcceptKey computes Sec-WebSocket-Accept as required by RFC 6455.
func WebSocketAcceptKey(key string) string {
	// RFC 6455 section 4.2.2 requires SHA-1 here as a wire-format operation;
	// Sec-WebSocket-Accept is not a password hash or security signature.
	digest := sha1.Sum([]byte(key + webSocketGUID)) // #nosec G505 -- protocol-mandated SHA-1
	return base64.StdEncoding.EncodeToString(digest[:])
}

// UpgradeWebSocket validates and completes a server-side HTTP upgrade.
func UpgradeWebSocket(w http.ResponseWriter, request *http.Request) (*WebSocketConn, error) {
	if err := validateUpgrade(request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, err
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		err := fmt.Errorf("websocket hijacking unsupported")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, err
	}
	conn, stream, err := hijacker.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack websocket: %w", err)
	}
	if err := writeUpgradeResponse(stream, request.Header.Get("Sec-WebSocket-Key")); err != nil {
		conn.Close() //nolint:errcheck
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close() //nolint:errcheck
		return nil, fmt.Errorf("clear websocket handshake deadline: %w", err)
	}
	return &WebSocketConn{conn: conn, reader: stream.Reader, expectMask: true}, nil
}

func validateUpgrade(request *http.Request) error {
	if request.Method != http.MethodGet {
		return fmt.Errorf("websocket upgrade requires GET")
	}
	if !headerHasToken(request.Header, "Connection", "upgrade") || !headerHasToken(request.Header, "Upgrade", "websocket") {
		return fmt.Errorf("invalid websocket upgrade headers")
	}
	if request.Header.Get("Sec-WebSocket-Version") != "13" {
		return fmt.Errorf("unsupported websocket version")
	}
	decoded, err := base64.StdEncoding.DecodeString(request.Header.Get("Sec-WebSocket-Key"))
	if err != nil || len(decoded) != 16 {
		return fmt.Errorf("invalid websocket key")
	}
	return nil
}

func headerHasToken(header http.Header, name, expected string) bool {
	for _, value := range header.Values(name) {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), expected) {
				return true
			}
		}
	}
	return false
}

func writeUpgradeResponse(stream *bufio.ReadWriter, key string) error {
	_, err := fmt.Fprintf(stream, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", WebSocketAcceptKey(key))
	if err != nil {
		return fmt.Errorf("write websocket upgrade: %w", err)
	}
	if err := stream.Flush(); err != nil {
		return fmt.Errorf("flush websocket upgrade: %w", err)
	}
	return nil
}

// DialWebSocket opens and verifies a client-side ws:// connection.
func DialWebSocket(ctx context.Context, address string) (*WebSocketConn, error) {
	target, err := url.Parse(address)
	if err != nil || target.Scheme != "ws" || target.Host == "" {
		return nil, fmt.Errorf("invalid websocket URL %q", address)
	}
	key, err := randomWebSocketKey()
	if err != nil {
		return nil, err
	}
	conn, err := dialWebSocketTarget(ctx, target)
	if err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	if err := clientUpgrade(ctx, conn, reader, target, key); err != nil {
		conn.Close() //nolint:errcheck
		return nil, err
	}
	return &WebSocketConn{conn: conn, reader: reader, maskWrites: true}, nil
}

func randomWebSocketKey() (string, error) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("generate websocket key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

func dialWebSocketTarget(ctx context.Context, target *url.URL) (net.Conn, error) {
	host := target.Host
	if target.Port() == "" {
		host = net.JoinHostPort(target.Hostname(), "80")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("connect websocket: %w", err)
	}
	return conn, nil
}

func clientUpgrade(ctx context.Context, conn net.Conn, reader *bufio.Reader, target *url.URL, key string) error {
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)          //nolint:errcheck
		defer conn.SetDeadline(time.Time{}) //nolint:errcheck
	}
	request := &http.Request{
		Method: http.MethodGet, URL: target, Host: target.Host,
		Header: http.Header{
			"Upgrade": {"websocket"}, "Connection": {"Upgrade"},
			"Sec-Websocket-Version": {"13"}, "Sec-Websocket-Key": {key},
		},
	}
	if err := request.Write(conn); err != nil {
		return fmt.Errorf("send websocket upgrade: %w", err)
	}
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return fmt.Errorf("read websocket upgrade: %w", err)
	}
	return validateUpgradeResponse(response, key)
}

func validateUpgradeResponse(response *http.Response, key string) error {
	if response.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("websocket upgrade returned %s", response.Status)
	}
	if !headerHasToken(response.Header, "Connection", "upgrade") || !headerHasToken(response.Header, "Upgrade", "websocket") {
		return fmt.Errorf("invalid websocket upgrade response")
	}
	if response.Header.Get("Sec-WebSocket-Accept") != WebSocketAcceptKey(key) {
		return fmt.Errorf("invalid websocket accept key")
	}
	return nil
}

// ReadText reads the next text message, transparently answering ping frames.
func (c *WebSocketConn) ReadText() ([]byte, error) {
	for {
		opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case webSocketText:
			return payload, nil
		case webSocketPing:
			if err := c.writeFrame(webSocketPong, payload); err != nil {
				return nil, err
			}
		case webSocketPong:
			continue
		case webSocketClose:
			code := closeCode(payload)
			c.writeFrame(webSocketClose, payload) //nolint:errcheck
			return nil, WebSocketCloseError{Code: code}
		default:
			return nil, c.reject(webSocketUnsupported)
		}
	}
}

func (c *WebSocketConn) readFrame() (byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.reader, header); err != nil {
		return 0, nil, err
	}
	fin, opcode, masked := header[0]&0x80 != 0, header[0]&0x0f, header[1]&0x80 != 0
	if header[0]&0x70 != 0 || masked != c.expectMask {
		return 0, nil, c.reject(webSocketProtocol)
	}
	if !fin || opcode == 0 || opcode == 0x2 {
		return 0, nil, c.reject(webSocketUnsupported)
	}
	length, err := c.readLength(header[1]&0x7f, opcode)
	if err != nil {
		return 0, nil, err
	}
	return c.readPayload(opcode, length, masked)
}

func (c *WebSocketConn) readLength(indicator, opcode byte) (uint64, error) {
	length := uint64(indicator)
	var encoded []byte
	if indicator == 126 {
		encoded = make([]byte, 2)
	} else if indicator == 127 {
		encoded = make([]byte, 8)
	}
	if len(encoded) > 0 {
		if _, err := io.ReadFull(c.reader, encoded); err != nil {
			return 0, err
		}
		if len(encoded) == 2 {
			length = uint64(binary.BigEndian.Uint16(encoded))
		} else {
			length = binary.BigEndian.Uint64(encoded)
		}
	}
	if opcode >= webSocketClose && length > 125 {
		return 0, c.reject(webSocketProtocol)
	}
	if length > webSocketMaxFrame {
		return 0, c.reject(webSocketTooLarge)
	}
	return length, nil
}

func (c *WebSocketConn) readPayload(opcode byte, length uint64, masked bool) (byte, []byte, error) {
	mask := make([]byte, 4)
	if masked {
		if _, err := io.ReadFull(c.reader, mask); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

// WriteText emits one unfragmented UTF-8 text frame.
func (c *WebSocketConn) WriteText(payload []byte) error {
	if len(payload) > webSocketMaxFrame {
		return WebSocketCloseError{Code: webSocketTooLarge}
	}
	return c.writeFrame(webSocketText, payload)
}

func (c *WebSocketConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, byte(length))
	case length <= 65535:
		header = append(header, 126, byte(length>>8), byte(length))
	default:
		header = append(header, 127, 0, 0, 0, 0, byte(uint64(length)>>24), byte(uint64(length)>>16), byte(uint64(length)>>8), byte(length))
	}
	if c.maskWrites {
		return c.writeMasked(header, payload)
	}
	if _, err := c.conn.Write(append(header, payload...)); err != nil {
		return fmt.Errorf("write websocket frame: %w", err)
	}
	return nil
}

func (c *WebSocketConn) writeMasked(header, payload []byte) error {
	header[1] |= 0x80
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return fmt.Errorf("generate websocket mask: %w", err)
	}
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	frame := append(append(header, mask...), masked...)
	if _, err := c.conn.Write(frame); err != nil {
		return fmt.Errorf("write websocket frame: %w", err)
	}
	return nil
}

func (c *WebSocketConn) reject(code int) error {
	payload := []byte{byte(code >> 8), byte(code)}
	c.writeFrame(webSocketClose, payload) //nolint:errcheck
	return WebSocketCloseError{Code: code}
}

func closeCode(payload []byte) int {
	if len(payload) < 2 {
		return webSocketNormal
	}
	return int(binary.BigEndian.Uint16(payload[:2]))
}

// SetDeadline applies a read and write deadline to the underlying connection.
func (c *WebSocketConn) SetDeadline(deadline time.Time) error {
	return c.conn.SetDeadline(deadline)
}

// SetWriteDeadline bounds subsequent writes.
func (c *WebSocketConn) SetWriteDeadline(deadline time.Time) error {
	return c.conn.SetWriteDeadline(deadline)
}

// Close performs a normal close handshake and closes the network connection.
func (c *WebSocketConn) Close() error {
	var result error
	c.closeOnce.Do(func() {
		payload := []byte{byte(webSocketNormal >> 8), byte(webSocketNormal & 0xff)}
		c.writeFrame(webSocketClose, payload) //nolint:errcheck
		result = c.conn.Close()
	})
	return result
}

// IsWebSocketClose reports whether an error represents a close handshake.
func IsWebSocketClose(err error) bool {
	var closeError WebSocketCloseError
	return errors.As(err, &closeError) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)
}

// CleanClose reports whether a read ended through a close handshake or EOF.
func (*WebSocketConn) CleanClose(err error) bool {
	return IsWebSocketClose(err)
}
