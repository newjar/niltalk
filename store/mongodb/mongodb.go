package mongodb

import (
	"context"
	"fmt"
	"github.com/knadh/niltalk/store"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"time"
)

// Config represents the MongoDB store config structure.
type Config struct {
	Address     string        `koanf:"address"`
	Password    string        `koanf:"password"`
	Username    string        `koanf:"username"`
}

type MongoDB struct {
	cfg     *Config
	mongodb *mongo.Database
}

type message struct {
	Time time.Time `json:"time" bson:"_id"`
	RoomID string `json:"room_id" bson:"room_id"`
	Payload []byte `json:"payload" bson:"payload"`
}

const (
	MESSAGE_CACHE_COLLECTION string = "message_cache"
)

func (m *MongoDB) AddMessageCache(payload store.Message) error {
	_, err := m.mongodb.Collection(MESSAGE_CACHE_COLLECTION).InsertOne(context.Background(),message{
		Time:    payload.Time.UTC(),
		RoomID:  payload.RoomID,
		Payload: payload.Payload,
	})

	return err
}

func (m *MongoDB) GetMessageCache(roomID string, limit int) ([]store.Message,error) {
	cur, err := m.mongodb.Collection(MESSAGE_CACHE_COLLECTION).Aggregate(context.Background(),mongo.Pipeline{
		bson.D{
			{
				"$match",
				bson.D{
					{"room_id",roomID},
				},
			},
		},
		bson.D{
			{
				"$sort",
				bson.D{
					{"_id",-1},
				},
			},
		},
		bson.D{
			{
				"$limit",
				limit,
			},
		},
		bson.D{
			{
				"$sort",
				bson.D{
					{"_id",1},
				},
			},
		},
	})

	if err != nil {
		return nil, err
	}

	defer cur.Close(context.Background())

	var results []store.Message

	for cur.Next(context.Background()) {
		var result message

		err = cur.Decode(&result)

		if err != nil {
			return nil, err
		}

		results = append(results,store.Message{
			Time:    result.Time.UTC(),
			RoomID:  result.RoomID,
			Payload: result.Payload,
		})
	}

	err = cur.Err()

	return results, err
}

func New (cfg Config) (*MongoDB, error) {
	username_password := ""

	if cfg.Username != "" {
		username_password = fmt.Sprintf("%s:%s@",cfg.Username,cfg.Password)
	}

	time.Sleep(time.Second * 10)

	ctx, _ := context.WithTimeout(context.Background(),10 * time.Minute)
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(fmt.Sprintf("mongodb://%s%s/",username_password,cfg.Address)))

	if err != nil {
		return nil, err
	}

	pingctx, _ := context.WithTimeout(context.Background(),30 * time.Second)
	err = client.Ping(pingctx, readpref.Primary())

	if err != nil {
		return nil, err
	}

	return &MongoDB{
		cfg:&cfg,
		mongodb:client.Database("niltalk"),
	}, nil
}