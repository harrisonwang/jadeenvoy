package store

import (
	"crypto/rand"
	"strings"

	"github.com/oklog/ulid/v2"
)

// NewID 生成 ULID + 前缀。例: NewID("agt") => "agt-01HXY..."
func NewID(prefix string) string {
	id := ulid.Make()
	return prefix + "-" + strings.ToLower(id.String())
}

var _ = rand.Reader // 保留 import；ulid 内部用
