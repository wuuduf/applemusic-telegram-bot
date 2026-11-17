package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wuuduf/applemusic-telegram-bot/utils/structs"
)

func TestLoadConfigMetadataStorefrontOptIn(t *testing.T) {
	oldConfig := Config
	defer func() { Config = oldConfig }()
	for _, tc := range []struct {
		name string
		yaml string
		want string
	}{
		{"omitted", "storefront: tr\n", ""},
		{"disabled", "storefront: tr\nmetadata-storefront: \"\"\n", ""},
		{"Chinese", "storefront: tr\nmetadata-storefront: CN\n", "cn"},
		{"normalized", "storefront: tr\nmetadata-storefront: \" TW \"\n", "tw"},
		{"invalid", "storefront: tr\nmetadata-storefront: invalid\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			Config = structs.ConfigSet{}
			dir := t.TempDir()
			t.Chdir(dir)
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := loadConfig(); err != nil {
				t.Fatal(err)
			}
			field := reflect.ValueOf(Config).FieldByName("MetadataStorefront")
			got := ""
			if field.IsValid() {
				got = field.String()
			}
			if got != tc.want {
				t.Fatalf("metadata-storefront = %q, want %q (cross-storefront config support missing or normalization incorrect)", got, tc.want)
			}
			if Config.Storefront != "tr" {
				t.Fatalf("audio storefront changed to %q, want tr", Config.Storefront)
			}
		})
	}
}
