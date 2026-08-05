package loadtest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// TreeOptions describes a deterministic directory tree. Count is the number
// of descendants to generate; the tenant root is not included in Count.
type TreeOptions struct {
	TenantID string
	RootID   string
	Count    int64
	Fanout   int64
	Seed     string
}

type TreeNode struct {
	Index          int64
	ID             string
	ParentID       string
	DisplayName    string
	NormalizedName string
}

func (o TreeOptions) Validate() error {
	if strings.TrimSpace(o.TenantID) == "" {
		return fmt.Errorf("tenant id is required")
	}
	if strings.TrimSpace(o.RootID) == "" {
		return fmt.Errorf("root id is required")
	}
	if o.Count < 0 {
		return fmt.Errorf("count must be non-negative")
	}
	if o.Fanout < 1 || o.Fanout > 10000 {
		return fmt.Errorf("fanout must be between 1 and 10000")
	}
	if strings.TrimSpace(o.Seed) == "" {
		return fmt.Errorf("seed is required")
	}
	if len(o.Seed) > 200 {
		return fmt.Errorf("seed must be at most 200 bytes")
	}
	for _, character := range o.Seed {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '.' && character != '_' && character != '-' {
			return fmt.Errorf("seed may contain only letters, digits, dot, underscore, and hyphen")
		}
	}
	return nil
}

// DeterministicUUID returns a stable UUID-shaped identifier derived from a
// seed and index. The version/variant bits make it easy to recognize in tools,
// while retaining deterministic replay across machines.
func DeterministicUUID(seed string, index int64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", seed, index)))
	b := digest[:16]
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(b)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

// Generate calls fn in parent-before-child order. It allocates one node at a
// time so a million-node run remains bounded by the configured batch size in
// the database loader.
func Generate(options TreeOptions, fn func(TreeNode) error) error {
	if err := options.Validate(); err != nil {
		return err
	}
	idSeed := options.TenantID + ":" + options.Seed
	for index := int64(1); index <= options.Count; index++ {
		var parent string
		if index <= options.Fanout {
			parent = options.RootID
		} else {
			parent = DeterministicUUID(idSeed, (index-1)/options.Fanout)
		}
		name := fmt.Sprintf("%s-node-%010d", options.Seed, index)
		if err := fn(TreeNode{
			Index: index, ID: DeterministicUUID(idSeed, index), ParentID: parent,
			DisplayName: name, NormalizedName: strings.ToLower(name),
		}); err != nil {
			return err
		}
	}
	return nil
}
