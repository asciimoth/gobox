package debug

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"github.com/asciimoth/gobox"
)

func (instance *sandbox) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if err := gobox.ValidateEndpoint(network, address, false); err != nil {
		return nil, err
	}
	if err := instance.available(ctx); err != nil {
		return nil, err
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	tracked := &trackedConn{Conn: connection, instance: instance}
	if err = instance.track(tracked); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return tracked, nil
}

func (instance *sandbox) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	if err := gobox.ValidateEndpoint(network, address, true); err != nil {
		return nil, err
	}
	if err := instance.available(ctx); err != nil {
		return nil, err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, network, address)
	if err != nil {
		return nil, err
	}
	tracked := &trackedListener{Listener: listener, instance: instance}
	if err = instance.track(tracked); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return tracked, nil
}

func (instance *sandbox) ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error) {
	if err := gobox.ValidateEndpoint(network, address, true); err != nil {
		return nil, err
	}
	if err := instance.available(ctx); err != nil {
		return nil, err
	}
	packet, err := (&net.ListenConfig{}).ListenPacket(ctx, network, address)
	if err != nil {
		return nil, err
	}
	tracked := &trackedPacketConn{PacketConn: packet, instance: instance, network: network}
	if connection, ok := packet.(net.Conn); ok {
		tracked.connection = connection
	}
	if err = instance.track(tracked); err != nil {
		_ = packet.Close()
		return nil, err
	}
	return tracked, nil
}

func (instance *sandbox) DialUnix(ctx context.Context, socketPath gobox.Path) (net.Conn, error) {
	if !supportsUnixSockets() {
		return nil, gobox.ErrUnsupported
	}
	if err := instance.available(ctx); err != nil {
		return nil, err
	}
	name, err := instance.unixPath(socketPath)
	if err != nil {
		return nil, err
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", name)
	if err != nil {
		return nil, err
	}
	tracked := &trackedConn{Conn: connection, instance: instance}
	if err = instance.track(tracked); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return tracked, nil
}

func (instance *sandbox) ListenUnix(ctx context.Context, socketPath gobox.Path) (net.Listener, error) {
	if !supportsUnixSockets() {
		return nil, gobox.ErrUnsupported
	}
	if err := instance.available(ctx); err != nil {
		return nil, err
	}
	name, err := instance.unixPath(socketPath)
	if err != nil {
		return nil, err
	}
	if _, err = os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return nil, gobox.ErrConflict
		}
		return nil, err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", name)
	if err != nil {
		return nil, err
	}
	tracked := &trackedListener{Listener: listener, instance: instance, unixPath: name}
	if err = instance.track(tracked); err != nil {
		_ = listener.Close()
		_ = os.Remove(name)
		return nil, err
	}
	return tracked, nil
}

func (instance *sandbox) unixPath(value gobox.Path) (string, error) {
	if err := gobox.ValidatePath(value, false); err != nil {
		return "", err
	}
	return instance.ChildPath(value)
}

func (instance *sandbox) available(ctx context.Context) error {
	if err := ctxError(ctx); err != nil {
		return err
	}
	instance.mu.Lock()
	defer instance.mu.Unlock()
	if instance.closed {
		return gobox.ErrClosed
	}
	return nil
}

type trackedConn struct {
	net.Conn
	instance *sandbox
	once     sync.Once
	err      error
}

func (connection *trackedConn) Close() error {
	connection.once.Do(func() {
		connection.err = connection.Conn.Close()
		connection.instance.untrack(connection)
	})
	return connection.err
}

type trackedListener struct {
	net.Listener
	instance *sandbox
	unixPath string
	once     sync.Once
	err      error
}

func (listener *trackedListener) Accept() (net.Conn, error) {
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tracked := &trackedConn{Conn: connection, instance: listener.instance}
	if err = listener.instance.track(tracked); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return tracked, nil
}

func (listener *trackedListener) Close() error {
	listener.once.Do(func() {
		closeErr := listener.Listener.Close()
		var removeErr error
		if listener.unixPath != "" {
			removeErr = os.Remove(listener.unixPath)
			if errors.Is(removeErr, os.ErrNotExist) {
				removeErr = nil
			}
		}
		listener.err = errors.Join(closeErr, removeErr)
		listener.instance.untrack(listener)
	})
	return listener.err
}

type trackedPacketConn struct {
	net.PacketConn
	connection net.Conn
	instance   *sandbox
	network    string
	once       sync.Once
	err        error
}

func (packet *trackedPacketConn) WriteTo(data []byte, address net.Addr) (int, error) {
	if address == nil {
		return 0, gobox.ErrInvalid
	}
	if err := gobox.ValidateEndpoint(packet.network, address.String(), false); err != nil {
		return 0, err
	}
	return packet.PacketConn.WriteTo(data, address)
}

func (packet *trackedPacketConn) Read(data []byte) (int, error) {
	if packet.connection == nil {
		return 0, gobox.ErrUnsupported
	}
	return packet.connection.Read(data)
}

func (packet *trackedPacketConn) Write(data []byte) (int, error) {
	if packet.connection == nil {
		return 0, gobox.ErrUnsupported
	}
	return packet.connection.Write(data)
}

func (packet *trackedPacketConn) RemoteAddr() net.Addr {
	if packet.connection == nil {
		return nil
	}
	return packet.connection.RemoteAddr()
}

func (packet *trackedPacketConn) SetDeadline(deadline time.Time) error {
	return packet.PacketConn.SetDeadline(deadline)
}

func (packet *trackedPacketConn) SetReadDeadline(deadline time.Time) error {
	return packet.PacketConn.SetReadDeadline(deadline)
}

func (packet *trackedPacketConn) SetWriteDeadline(deadline time.Time) error {
	return packet.PacketConn.SetWriteDeadline(deadline)
}

func (packet *trackedPacketConn) Close() error {
	packet.once.Do(func() {
		packet.err = packet.PacketConn.Close()
		packet.instance.untrack(packet)
	})
	return packet.err
}

var _ net.Conn = (*trackedPacketConn)(nil)
