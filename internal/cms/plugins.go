package cms

import (
	"github.com/Martin-Business-Consultants/librepublishgo/internal/plugin"
	"github.com/Martin-Business-Consultants/librepublishgo/plugins/forms"
	"github.com/Martin-Business-Consultants/librepublishgo/plugins/fuse"
	"github.com/Martin-Business-Consultants/librepublishgo/plugins/launch"
)

// builtinPlugins are the plugins this binary has. Adding one is adding its
// package here; the default ones are installed and switched on at first
// run (plugin.Host.EnsureDefaults), the rest wait for someone to install
// them from Plugins (Fuse pricing, which a site without quotes has no use
// for).
func builtinPlugins() []*plugin.Module {
	return []*plugin.Module{forms.Module(), launch.Module(), fuse.Module()}
}
