package requestid

import (
	"context"

	"github.com/google/uuid"
)

type contextKey string

const Key contextKey = "request_id"

func NewID() string {
	return uuid.New().String()
}

func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(Key).(string)
	return id
}

func ToContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, Key, id)
}
