package store

import (
	"errors"
	"time"
)

// Store represents a backend store.
type Store interface {
	AddRoom(r Room, ttl time.Duration) error
	GetRoom(id string) (Room, error)
	ExtendRoomTTL(id string, ttl time.Duration) error
	RoomExists(id string) (bool, error)
	RemoveRoom(id string) error

	AddSession(sessID, handle, roomID string, ttl time.Duration) error
	GetSession(sessID, roomID string) (Sess, error)
	RemoveSession(sessID, roomID string) error
	ClearSessions(roomID string) error
}

type MessageCache interface {
	AddMessageCache(payload Message) error
	GetMessageCache(roomID string, limit int, dateFilter DateFilter) ([]Message,error)
}

// Room represents the properties of a room in the store.
type Room struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Password  []byte    `json:"password"`
	CreatedAt time.Time `json:"created_at"`
}

// Sess represents an authenticated peer session.
type Sess struct {
	ID     string `json:"id"`
	Handle string `json:"name"`
}

type Message struct {
	Time time.Time `json:"time"`
	RoomID string `json:"room_id"`
	Payload []byte `json:"payload"`
}

type DateFilter struct {
	Start time.Time
	End time.Time
}

// ErrRoomNotFound indicates that the requested room was not found.
var ErrRoomNotFound = errors.New("room not found")
