//go:build linux

package wayland

func (c *Conn) AllocIDForDebug(iface string) uint32 { return c.allocID(iface) }
func (c *Conn) SendForDebug(obj, op uint32, e *encoder) error { return c.send(obj, op, e) }

type DebugEvent struct {
	ObjectID uint32
	Opcode   uint16
	Args     []byte
}

func (c *Conn) RecvForDebug() (*DebugEvent, error) {
	ev, err := c.recvOne()
	if err != nil {
		return nil, err
	}
	return &DebugEvent{ObjectID: ev.objectID, Opcode: ev.opcode, Args: ev.args}, nil
}
