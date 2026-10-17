// Package protocol defines the wire format spoken between the relay and its
// agents: the TLS handshake (Hello / Reply), the yamux stream headers and the
// report, push and ack messages carried on those streams. Every message is a
// length-prefixed JSON frame (see WriteMsg / ReadMsg).
package protocol

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

// Version is the wire protocol revision; both sides must speak the same one.
const Version = 5

// AppVersion is the build version reported in the handshake.
const AppVersion = "1.0.0"

// Hello purposes.
const (
	PurposeConnect = "connect"
	PurposeInit    = "init"
)

// Machine roles.
const (
	RoleAdmin  = "admin"
	RoleClient = "client"
)

// Handshake error codes.
const (
	CodeLocked       = "locked"
	CodeUnauthorized = "unauthorized"
	CodeDBExists     = "db_exists"
	CodeBadInit      = "bad_init"
	CodeVersion      = "version"
	CodeInternal     = "internal"
)

// Stream types: the first frame of every yamux stream is a StreamHeader
// whose T is one of these.
const (
	// StreamReport: agent → server, periodic Report frames.
	StreamReport = "report"
	// StreamAdmin: agent → server reverse tunnel for the admin ui.
	StreamAdmin = "admin"
	// StreamRoute: agent → server, carries a connection that entered on an
	// agent-side ingress port; the server resolves the route.
	StreamRoute = "route"
	// StreamDial: server → agent, asks the agent to execute an egress spec.
	StreamDial = "dial"
	// StreamPush: server → agent, pushes the port configuration.
	StreamPush = "push"
)

// Endpoint types.
const (
	TypeDirect = "direct"
	TypeSocks5 = "socks5"
)

// Hello is the first frame an agent sends after the TLS handshake.
type Hello struct {
	Version      int    `json:"version"`
	Purpose      string `json:"purpose"`
	Token        string `json:"token,omitempty"`
	DBPassword   string `json:"db_password,omitempty"`
	AdminToken   string `json:"admin_token,omitempty"`
	AgentVersion string `json:"agent_version,omitempty"`
}

// Reply is the relay's answer to a Hello. On success Error is empty and Role
// is set; AdminToken is sent only to admin-role machines when the relay knows
// the plaintext, so the machine can open its local admin UI.
type Reply struct {
	Role       string `json:"role,omitempty"`
	Version    string `json:"version,omitempty"`
	AdminToken string `json:"admin_token,omitempty"`
	Error      *Error `json:"error,omitempty"`
}

// Error is a handshake rejection reported by the relay.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("server error (%s): %s", e.Code, e.Message)
}

// Handshake sends hello and reads the relay's reply. A rejection is returned
// as a *Error.
func Handshake(rw io.ReadWriter, hello Hello) (Reply, error) {
	hello.Version = Version
	if hello.AgentVersion == "" {
		hello.AgentVersion = AppVersion
	}
	if err := WriteMsg(rw, hello); err != nil {
		return Reply{}, fmt.Errorf("hello write: %w", err)
	}
	var r Reply
	if err := ReadMsg(rw, &r); err != nil {
		return Reply{}, fmt.Errorf("hello read: %w", err)
	}
	if r.Error != nil {
		return Reply{}, r.Error
	}
	return r, nil
}

// StreamHeader is the first frame on every yamux stream.
type StreamHeader struct {
	T string `json:"t"`
	// InPortID identifies the ingress port (StreamRoute only).
	InPortID int64 `json:"inport_id,omitempty"`
	// Egress is the spec to execute (StreamDial only).
	Egress *EgressSpec `json:"egress,omitempty"`
}

// EgressSpec tells a machine how to open the final leg of a connection.
// Allow/Deny are destination filters checked before the dial.
type EgressSpec struct {
	Machine    string   `json:"machine"`
	Type       string   `json:"type"`
	Host       string   `json:"host"`
	Port       int      `json:"port"`
	Allow      []string `json:"allow,omitempty"`
	Deny       []string `json:"deny,omitempty"`
	Socks5User string   `json:"socks5_user,omitempty"`
	Socks5Pass string   `json:"socks5_pass,omitempty"`
}

// Ingress is one port an agent must bind. Only routed ports are sent.
// Allow/Deny are source filters checked when a connection arrives.
type Ingress struct {
	InPortID int64      `json:"inport_id"`
	Port     int        `json:"port"`
	Label    string     `json:"label,omitempty"`
	Allow    []string   `json:"allow,omitempty"`
	Deny     []string   `json:"deny,omitempty"`
	Egress   EgressSpec `json:"egress"`
}

// EgressInfo describes an egress endpoint served by this machine and who
// routes into it (informational, for the agent log).
type EgressInfo struct {
	EndpointID int64    `json:"endpoint_id"`
	Type       string   `json:"type"`
	Target     string   `json:"target"`
	Label      string   `json:"label,omitempty"`
	From       []string `json:"from,omitempty"`
}

// ConfigPush is the whole slice of configuration one machine needs.
type ConfigPush struct {
	Version int64        `json:"version"`
	In      []Ingress    `json:"in"`
	Out     []EgressInfo `json:"out"`
}

// ConfigAck is the agent reply to a ConfigPush. Error rejects the whole push;
// BindErrors lists ports the agent could not open (the rest is applied).
type ConfigAck struct {
	Version    int64       `json:"version"`
	Error      string      `json:"error,omitempty"`
	BindErrors []BindError `json:"bind_errors,omitempty"`
}

// BindError reports one ingress port the agent failed to bind.
type BindError struct {
	InPortID int64  `json:"inport_id"`
	Port     int    `json:"port"`
	Label    string `json:"label,omitempty"`
	Error    string `json:"error"`
}

// Report is the periodic frame an agent sends on its report stream: counter
// deltas, seen addresses and live connections of its ingress ports.
type Report struct {
	Stats []StatDelta `json:"stats,omitempty"`
	Peers []PeerDelta `json:"peers,omitempty"`
	Live  []LiveConn  `json:"live,omitempty"`
}

// LiveConn is one active connection seen by an agent-side ingress port.
type LiveConn struct {
	InPortID int64  `json:"inport_id"`
	Remote   string `json:"remote"`
	Since    int64  `json:"since"`
}

// StatDelta carries counter deltas of one agent-side ingress port.
type StatDelta struct {
	InPortID int64 `json:"inport_id"`
	BytesIn  int64 `json:"bytes_in"`
	BytesOut int64 `json:"bytes_out"`
	Conns    int64 `json:"conns"`
	Active   int   `json:"active"`
}

// PeerDelta reports traffic of one remote address on an agent-side port.
type PeerDelta struct {
	IP       string `json:"ip"`
	InPortID int64  `json:"inport_id"`
	Conns    int64  `json:"conns"`
	BytesIn  int64  `json:"bytes_in"`
	BytesOut int64  `json:"bytes_out"`
}

const maxFrame = 1 << 20

// WriteMsg writes v as one length-prefixed JSON frame.
func WriteMsg(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) > maxFrame {
		return fmt.Errorf("protocol: frame too large: %d", len(data))
	}
	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame, uint32(len(data)))
	copy(frame[4:], data)
	_, err = w.Write(frame)
	return err
}

// ReadMsg reads one length-prefixed JSON frame into v.
func ReadMsg(r io.Reader, v any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > maxFrame {
		return fmt.Errorf("protocol: invalid frame size %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return json.Unmarshal(buf, v)
}

// NewToken returns a random url-safe token with the given prefix.
func NewToken(prefix string) string { return prefix + randomString(24) }

// NewPassword returns a random url-safe password (256 bits).
func NewPassword() string { return randomString(32) }

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// KeyHash is the hex sha256 of a token: the identity stored by the relay.
func KeyHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
