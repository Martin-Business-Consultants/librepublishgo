package cms

import (
	"fmt"
	"strings"
	"time"

	"github.com/Martin-Business-Consultants/librepublishgo/internal/apierr"
)

// deployJSON is the deploy as the API shows it. The build hook runs a
// deploy for anyone who has it, so who can't change it sees it masked,
// in the config and in a failed build's error alike.
func (a *App) deployJSON(c *Ctx) (Obj, error) {
	ctx := c.R.Context()
	cfg, err := a.setting(ctx, "deploy")
	if err != nil {
		return nil, err
	}
	status, err := a.setting(ctx, "deploy_status")
	if err != nil {
		return nil, err
	}
	front, err := a.setting(ctx, "frontend")
	if err != nil {
		return nil, err
	}
	config := redact(cfg)
	if !c.can("settings:write") {
		config = redactURLs(config)
		hideHook(status, str(cfg["hook_url"]))
	}
	return O("config", config, "frontend", redact(front), "last", status,
		"configured", str(cfg["hook_url"]) != "" || str(front["webhook_url"]) != ""), nil
}

func (a *App) showDeploy(c *Ctx) error {
	if err := c.Need("settings:read"); err != nil {
		return err
	}
	d, err := a.deployJSON(c)
	if err != nil {
		return err
	}
	if last, ok := d.Get("last").(map[string]any); ok && len(last) > 0 {
		c.Summary = fmt.Sprintf("Last deploy: %s at %s.", str(last["status"]), str(last["at"]))
	} else {
		c.Summary = "No deploy has run yet."
	}
	c.Crumbs = []Crumb{step("Rebuild now", "trigger_deploy")}
	return c.JSON(200, O("deploy", d))
}

func (a *App) updateDeploy(c *Ctx) error {
	if err := c.Need("settings:write"); err != nil {
		return err
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	data := body
	if d, ok := body["deploy"].(map[string]any); ok {
		data = d
	}
	if err := a.needSettingAccess(c, "deploy", data, false); err != nil {
		return err
	}
	if _, err := a.putSetting(c.R.Context(), c.Actor, "deploy", data, false); err != nil {
		return err
	}
	d, err := a.deployJSON(c)
	if err != nil {
		return err
	}
	return c.JSON(200, O("deploy", d))
}

func (a *App) triggerDeploy(c *Ctx) error {
	if err := c.Need("pages:publish"); err != nil {
		return err
	}
	status, err := a.deploys.Trigger(c.R.Context(), "manual")
	a.audit(c.R.Context(), c.Actor, "deploy.triggered", "Deploy", 0, "manual", nil)
	if err != nil {
		msg := err.Error()
		if !c.can("settings:write") {
			hook := str(a.settingOr(c.R.Context(), "deploy")["hook_url"])
			hideHook(status, hook)
			if hook != "" {
				msg = strings.ReplaceAll(msg, hook, maskURL(hook))
			}
		}
		return &apierr.Error{Status: 502, Code: "deploy_failed", Message: msg, Extra: map[string]any{"deploy": status}}
	}
	c.Summary = "Deploy " + str(status["status"]) + "."
	c.Crumbs = []Crumb{step("Whether it worked", "get_deploy")}
	return c.JSON(202, O("ok", true, "deploy", status))
}

// hideHook masks the build hook in a deploy's status: a failed post's
// error names the address it went to.
func hideHook(status map[string]any, hook string) {
	if e := str(status["error"]); hook != "" && e != "" {
		status["error"] = strings.ReplaceAll(e, hook, maskURL(hook))
	}
}

func (a *App) showFrontend(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	front, err := a.setting(c.R.Context(), "frontend")
	if err != nil {
		return err
	}
	build, err := a.setting(c.R.Context(), "frontend_build")
	if err != nil {
		return err
	}
	return c.JSON(200, O("frontend", redact(front), "last_build", build))
}

// reportBuild: a build says how it renders and where purges go. Keeping a
// static site rebuilt on publish is taken as is; switching to purges, or
// moving where they go, waits for someone with settings:write.
func (a *App) reportBuild(c *Ctx) error {
	if err := c.Need("pages:read"); err != nil {
		return err
	}
	var body map[string]any
	if err := c.Body(&body); err != nil {
		return err
	}
	report := map[string]any{"render": body["render"], "webhook_url": body["webhook_url"], "content_cursor": body["content_cursor"],
		"reported_at": time.Now().UTC().Format(time.RFC3339), "by": c.Actor.Label(),
		"integration": clip(str(body["integration"]), 100), "integration_version": clip(str(body["integration_version"]), 50),
		"checks": buildChecks(body["checks"]), "checks_url": httpURL(str(body["checks_url"]))}
	if _, err := a.putSetting(c.R.Context(), nil, "frontend_build", report, true); err != nil {
		return err
	}
	front, err := a.setting(c.R.Context(), "frontend")
	if err != nil {
		return err
	}
	pending := str(body["render"]) != "static" && (str(front["render"]) != str(body["render"]) || str(front["webhook_url"]) != str(body["webhook_url"]))
	if pending && c.can("settings:write") {
		if _, err := a.putSetting(c.R.Context(), c.Actor, "frontend", map[string]any{"render": body["render"], "webhook_url": body["webhook_url"]}, false); err != nil {
			return err
		}
		pending = false
	}
	return c.JSON(201, O("ok", true, "approval_pending", pending))
}
