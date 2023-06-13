package ap

type PacketType byte

const (
	PacketTypeLogin       PacketType = 0xab
	PacketTypeAPWelcome   PacketType = 0xac
	PacketTypeAuthFailure PacketType = 0xad
)
