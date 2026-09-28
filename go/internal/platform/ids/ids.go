package ids

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/mr-tron/base58"
)

const (
	Agent   = "agt_"
	Version = "ver_"
	APIKey  = "key_"
	User    = "usr_"
	Tenant  = "ten_"
	Skill   = "skl_"
)

// Format 把 uuid 编成带资源前缀的 base58。前缀字节 0xff 避免 base58 丢掉前导 0。
func Format(prefix string, id uuid.UUID) string {
	buf := append([]byte{0xff}, id[:]...)
	return prefix + base58.Encode(buf)
}

func Parse(prefix, value string) (uuid.UUID, error) {
	if !strings.HasPrefix(value, prefix) {
		return uuid.Nil, fmt.Errorf("id prefix")
	}
	raw, err := base58.Decode(strings.TrimPrefix(value, prefix))
	if err != nil || len(raw) != 17 || raw[0] != 0xff {
		return uuid.Nil, fmt.Errorf("id encoding")
	}
	return uuid.FromBytes(raw[1:])
}
