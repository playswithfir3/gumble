package gumble

import (
	"fmt"
	"crypto/tls"
	"errors"
	"math"
	"net"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/golang/protobuf/proto"
	"layeh.com/gumble/gumble/MumbleProto"
)

// VersionOverride controls the initial Version message sent during the TLS handshake.
// Add `VersionOverride *VersionOverride` to your Config type.
type VersionOverride struct {
	Release       string   // e.g. "my-bot/2.3"
	OS            string   // e.g. "linux" or "windows"
	OSVersion     string   // e.g. "amd64"
	Semver        string   // "MAJOR.MINOR.PATCH" -> packed (maj<<16 | min<<8 | pat)
	VersionUint32 *uint32  // direct override if you already have the packed value
}

// packSemver turns "MAJOR.MINOR.PATCH" into the uint32 used in Mumble's Version.
func packSemver(s string) (uint32, error) {
	var maj, min, pat uint32
	n, err := fmt.Sscanf(s, "%d.%d.%d", &maj, &min, &pat)
	if err != nil || n != 3 || maj > 0xFFFF || min > 0xFF || pat > 0xFF {
		return 0, fmt.Errorf("invalid semver %q", s)
	}
	return (maj<<16 | min<<8 | pat), nil
}

// State is the current state of the client's connection to the server.
type State int

const (
	// StateDisconnected means the client is no longer connected to the server.
	StateDisconnected State = iota

	// StateConnected means the client is connected to the server and is
	// syncing initial information. This is an internal state that will
	// never be returned by Client.State().
	StateConnected

	// StateSynced means the client is connected to a server and has been sent
	// the server state.
	StateSynced
)

// ClientVersion is the protocol version that Client implements.
const ClientVersion = 1<<16 | 3<<8 | 0

// Client is the type used to create a connection to a server.
type Client struct {
	// The User associated with the client.
	Self *User
	// The client's configuration.
	Config *Config
	// The underlying Conn to the server.
	Conn *Conn

	// The users currently connected to the server.
	Users Users
	// The connected server's channels.
	Channels    Channels
	permissions map[uint32]*Permission
	tmpACL      *ACL

	// Ping stats
	tcpPacketsReceived uint32
	tcpPingTimes       [12]float32
	tcpPingAvg         uint32
	tcpPingVar         uint32

	// A collection containing the server's context actions.
	ContextActions ContextActions

	// The audio encoder used when sending audio to the server.
	AudioEncoder AudioEncoder
	audioCodec   AudioCodec
	// To whom transmitted audio will be sent. The VoiceTarget must have already
	// been sent to the server for targeting to work correctly. Setting to nil
	// will disable voice targeting (i.e. switch back to regular speaking).
	VoiceTarget *VoiceTarget

	state uint32

	// volatile is held by the client when the internal data structures are being
	// modified.
	volatile rpwMutex

	connect         chan *RejectError
	end             chan struct{}
	disconnectEvent DisconnectEvent
}

// Dial is an alias of DialWithDialer(new(net.Dialer), addr, config, nil).
func Dial(addr string, config *Config) (*Client, error) {
	return DialWithDialer(new(net.Dialer), addr, config, nil)
}

// DialWithDialer connects to the Mumble server at the given address.
//
// The function returns after the connection has been established, the initial
// server information has been synced, and the OnConnect handlers have been
// called.
//
// nil and an error is returned if server synchronization does not complete by
// min(time.Now() + dialer.Timeout, dialer.Deadline), or if the server rejects
// the client.
func DialWithDialer(dialer *net.Dialer, addr string, config *Config, tlsConfig *tls.Config) (*Client, error) {
	start := time.Now()

	conn, err := tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	if err != nil {
		return nil, err
	}

	client := &Client{
		Conn:     NewConn(conn),
		Config:   config,
		Users:    make(Users),
		Channels: make(Channels),

		permissions: make(map[uint32]*Permission),

		state: uint32(StateConnected),

		connect: make(chan *RejectError),
		end:     make(chan struct{}),
	}

	go client.readRoutine()

	// -------- Build the initial Version packet (with optional overrides) --------
	// Defaults reproduce original gumble behavior.
	release := "gumble"
	osStr := runtime.GOOS
	osVer := runtime.GOARCH
	verU32 := uint32(ClientVersion)

	if client.Config != nil && client.Config.VersionOverride != nil {
		vo := client.Config.VersionOverride
		if vo.Release != "" {
			release = vo.Release
		}
		if vo.OS != "" {
			osStr = vo.OS
		}
		if vo.OSVersion != "" {
			osVer = vo.OSVersion
		}
		if vo.VersionUint32 != nil {
			verU32 = *vo.VersionUint32
		} else if vo.Semver != "" {
			if packed, err := packSemver(vo.Semver); err == nil {
				verU32 = packed
			}
		}
	}

	versionPacket := MumbleProto.Version{
		Version:   proto.Uint32(verU32),
		Release:   proto.String(release),
		Os:        proto.String(osStr),
		OsVersion: proto.String(osVer),
	}

	authenticationPacket := MumbleProto.Authenticate{
		Username: &client.Config.Username,
		Password: &client.Config.Password,
		Opus:     proto.Bool(getAudioCodec(audioCodecIDOpus) != nil),
		Tokens:   client.Config.Tokens,
	}

	client.Conn.WriteProto(&versionPacket)
	client.Conn.WriteProto(&authenticationPacket)

	go client.pingRoutine()

	var timeout <-chan time.Time
	{
		var deadline time.Time
		if !dialer.Deadline.IsZero() {
			deadline = dialer.Deadline
		}
		if dialer.Timeout > 0 {
			diff := start.Add(dialer.Timeout)
			if deadline.IsZero() || diff.Before(deadline) {
				deadline = diff
			}
		}
		if !deadline.IsZero() {
			timer := time.NewTimer(deadline.Sub(start))
			defer timer.Stop()
			timeout = timer.C
		}
	}

	select {
	case <-timeout:
		client.Conn.Close()
		return nil, errors.New("gumble: synchronization timeout")
	case err := <-client.connect:
		if err != nil {
			client.Conn.Close()
			return nil, err
		}
		return client, nil
	}
}

// State returns the current state of the client.
func (c *Client) State() State {
	return State(atomic.LoadUint32(&c.state)
