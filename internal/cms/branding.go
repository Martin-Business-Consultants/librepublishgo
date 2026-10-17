package cms

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
)

// Branding is the site's visual identity (Settings › Brand), as the Rails
// core kept it under the "branding" setting: a logo and favicon from the
// media library, two colors, a font, corners, a shadow, and the admin's top
// bar. The site reads it from /api/v1/site; the admin wears it too (its
// colors, font, corners and shadow over its own tokens, the logo in its bar).
//
// It ends up in CSS, so every value is checked on the way in (a 422 names
// what's wrong) and again on the way out: imported data never was.
type Branding struct {
	LogoID, FaviconID int64
	PrimaryColor      string
	SecondaryColor    string
	Font              string
	BorderRadius      string
	BoxShadow         string
	TopBar            string
}

const brandingKey = "branding"

var (
	brandingFonts   = []string{"Inter", "DM Sans", "Manrope", "Playfair Display", "Lora"}
	brandingSizes   = []string{"none", "small", "medium", "large"}
	brandingTopBars = []string{"match", "dark"}
	hexColor        = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
)

// brandingOf reads the setting, keeping only values that pass the checks.
func brandingOf(data map[string]any) Branding {
	var b Branding
	b.LogoID, _ = intOf(data["logo_id"])
	b.FaviconID, _ = intOf(data["favicon_id"])
	pick := func(k string, ok func(string) bool) string {
		s := strings.TrimSpace(str(data[k]))
		if ok(s) {
			return s
		}
		return ""
	}
	oneOf := func(list []string) func(string) bool { return func(s string) bool { return slices.Contains(list, s) } }
	b.PrimaryColor = strings.ToLower(pick("primary_color", hexColor.MatchString))
	b.SecondaryColor = strings.ToLower(pick("secondary_color", hexColor.MatchString))
	b.Font = pick("font", oneOf(brandingFonts))
	b.BorderRadius = pick("border_radius", oneOf(brandingSizes))
	b.BoxShadow = pick("box_shadow", oneOf(brandingSizes))
	b.TopBar = pick("top_bar", oneOf(brandingTopBars))
	return b
}

// cleanBranding checks a write to the setting and gives what's stored: only
// the keys branding has, blanks dropped (a cleared field is cleared: the
// write replaces the setting, as Rails' Branding.save did), ids as numbers
// and colors in lowercase.
func (a *App) cleanBranding(ctx context.Context, data map[string]any) (map[string]any, error) {
	inv := apierr.Invalid{}
	out := map[string]any{}
	enum := func(k, s string, list []string) {
		if slices.Contains(list, s) {
			out[k] = s
		} else {
			inv.Add(k, "must be one of "+strings.Join(list, ", "))
		}
	}
	for k, v := range data {
		s, isStr := v.(string)
		s = strings.TrimSpace(s)
		if v == nil || (isStr && s == "") {
			continue
		}
		switch k {
		case "logo_id", "favicon_id":
			id, ok := intOf(v)
			if !ok || !a.isImageAsset(ctx, id) {
				inv.Add(k, "must be an image in the media library")
				continue
			}
			out[k] = id
		case "primary_color", "secondary_color":
			if !isStr || !hexColor.MatchString(s) {
				inv.Add(k, "must be a hex color, like #b45309")
				continue
			}
			out[k] = strings.ToLower(s)
		case "font":
			enum(k, s, brandingFonts)
		case "border_radius", "box_shadow":
			enum(k, s, brandingSizes)
		case "top_bar":
			enum(k, s, brandingTopBars)
		default:
			inv.Add(k, "isn't part of the branding: logo_id, favicon_id, primary_color, secondary_color, font, border_radius, box_shadow, top_bar")
		}
	}
	return out, inv.Err()
}

func (a *App) isImageAsset(ctx context.Context, id int64) bool {
	as, err := a.assetByID(ctx, id, false)
	return err == nil && as.Image()
}

// brandingImage is a branding logo or favicon that's still a live image.
func (a *App) brandingImage(ctx context.Context, id int64) *Asset {
	if id == 0 {
		return nil
	}
	as, err := a.assetByID(ctx, id, false)
	if err != nil || !as.Image() {
		return nil
	}
	return as
}

// brandingJSON is the branding as the site (and, withAdmin, the admin)
// reads it: the logo and favicon as delivered assets, and the values that
// are set. top_bar is the admin's alone.
func (a *App) brandingJSON(ctx context.Context, withAdmin bool) Obj {
	b := brandingOf(a.settingOr(ctx, brandingKey))
	o := O()
	if as := a.brandingImage(ctx, b.LogoID); as != nil {
		o = o.Set("logo", a.delivery(as))
	}
	if as := a.brandingImage(ctx, b.FaviconID); as != nil {
		o = o.Set("favicon", a.delivery(as))
	}
	for _, kv := range [][2]string{
		{"primary_color", b.PrimaryColor}, {"secondary_color", b.SecondaryColor}, {"font", b.Font},
		{"border_radius", b.BorderRadius}, {"box_shadow", b.BoxShadow},
	} {
		if kv[1] != "" {
			o = o.Set(kv[0], kv[1])
		}
	}
	if withAdmin && b.TopBar != "" {
		o = o.Set("top_bar", b.TopBar)
	}
	return o
}

// brandingUses is where the branding uses asset id, for "Used by".
func (a *App) brandingUses(ctx context.Context, id int64) []Obj {
	b := brandingOf(a.settingOr(ctx, brandingKey))
	out := []Obj{}
	for _, use := range []struct {
		id    int64
		label string
	}{{b.LogoID, "Logo"}, {b.FaviconID, "Favicon"}} {
		if use.id == id {
			out = append(out, O("type", "Setting", "id", brandingKey, "kind", strings.ToLower(use.label), "where", "Settings › Brand", "label", use.label, "path", "brand"))
		}
	}
	return out
}
