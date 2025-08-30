package gumble

import (
	"time"
)

// VersionOverride controls the initial Version message sent during the TLS handshake.
// If fields are empty/nil, gumble's defaults are used.
type VersionOverride struct {
    Release      string  // e.g. "my-bot/2.3"
    OS           string  // e.g. "windows" or "linux"
    OSVersion    string  // e.g. "amd64"
    // One of Semver or VersionUint32 may be used to set Version:
    Semver        string  // "MAJOR.MINOR.PATCH" -> packed (maj<<16 | min<<8 | pat)
    VersionUint32 *uint32 // direct override, if you already have the packed value
}

// Config holds the Mumble configuration used by Client. A single Config should
// not be shared between multiple Client instances.
type Config struct {
	// User name used when authenticating with the server.
	Username string
	// Password used when authenticating with the server. A password is not
	// usually required to connect to a server.
	Password string
	// The initial access tokens to the send to the server. Access tokens can be
	// resent to the server using:
	//  client.Send(config.Tokens)
    // If set, overrides the initial Version packet fields sent to the server.
    VersionOverride *VersionOverride
	
	Tokens AccessTokens

	// AudioInterval is the interval at which audio packets are sent. Valid
	// values are: 10ms, 20ms, 40ms, and 60ms.
	AudioInterval time.Duration
	// AudioDataBytes is the number of bytes that an audio frame can use.
	AudioDataBytes int

	// The event listeners used when client events are triggered.
	Listeners      Listeners
	AudioListeners AudioListeners
}

// NewConfig returns a new Config struct with default values set.
func NewConfig() *Config {
	return &Config{
		AudioInterval:  AudioDefaultInterval,
		AudioDataBytes: AudioDefaultDataBytes,
	}
}

// Attach is an alias of c.Listeners.Attach.
func (c *Config) Attach(l EventListener) Detacher {
	return c.Listeners.Attach(l)
}

// AttachAudio is an alias of c.AudioListeners.Attach.
func (c *Config) AttachAudio(l AudioListener) Detacher {
	return c.AudioListeners.Attach(l)
}

// AudioFrameSize returns the appropriate audio frame size, based off of the
// audio interval.
func (c *Config) AudioFrameSize() int {
	return int(c.AudioInterval/AudioDefaultInterval) * AudioDefaultFrameSize
}
