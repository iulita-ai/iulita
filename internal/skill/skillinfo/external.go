package skillinfo

import (
	"context"
	"fmt"
	"strings"
)

// actionSearchExternal searches a marketplace for external skills.
func (s *Skill) actionSearchExternal(ctx context.Context, source, query string, limit int) (string, error) {
	if err := requireAdmin(ctx); err != nil {
		return err.Error(), nil
	}
	if s.extMgr == nil {
		return "External skills are not enabled.", nil
	}
	if query == "" {
		return "query is required for search_external action.", nil
	}
	if source == "" {
		source = "clawhub"
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}

	results, err := s.extMgr.Search(ctx, source, query, limit)
	if err != nil {
		return fmt.Sprintf("Search failed: %v", err), nil
	}

	if len(results) == 0 {
		return fmt.Sprintf("No skills found for query %q on %s.", query, source), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d skill(s) on %s for %q:\n\n", len(results), source, query)
	for _, r := range results {
		fmt.Fprintf(&b, "- %s (%s)", r.Name, r.Slug)
		if r.Version != "" {
			fmt.Fprintf(&b, " v%s", r.Version)
		}
		if r.Author != "" {
			fmt.Fprintf(&b, " by %s", r.Author)
		}
		b.WriteString("\n")
		if r.Description != "" {
			fmt.Fprintf(&b, "  %s\n", r.Description)
		}
	}
	b.WriteString("\nTo install: action='install_external', ref='<slug>'")
	return b.String(), nil
}

// actionInstallExternal installs an external skill from a source.
func (s *Skill) actionInstallExternal(ctx context.Context, source, ref string) (string, error) {
	if err := requireAdmin(ctx); err != nil {
		return err.Error(), nil
	}
	if s.extMgr == nil {
		return "External skills are not enabled.", nil
	}
	if ref == "" {
		return "ref is required for install_external action (skill slug or URL).", nil
	}
	if source == "" {
		// Auto-detect source from ref format.
		if strings.HasPrefix(ref, "https://clawhub.ai/") {
			source = "clawhub"
		} else if strings.HasPrefix(ref, "https://") {
			source = "url"
		} else {
			source = "clawhub"
		}
	}

	installed, warnings, err := s.extMgr.Install(ctx, source, ref)
	if err != nil {
		return fmt.Sprintf("Install failed: %v", err), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Installed skill %q (%s) v%s from %s.\n", installed.Name, installed.Slug, installed.Version, source)
	fmt.Fprintf(&b, "Isolation: %s, Enabled: %v\n", installed.Isolation, installed.Enabled)

	if len(warnings) > 0 {
		b.WriteString("\nWarnings:\n")
		for _, w := range warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}

	return b.String(), nil
}

// actionUninstallExternal removes an installed external skill.
func (s *Skill) actionUninstallExternal(ctx context.Context, slug string) (string, error) {
	if err := requireAdmin(ctx); err != nil {
		return err.Error(), nil
	}
	if s.extMgr == nil {
		return "External skills are not enabled.", nil
	}
	if slug == "" {
		return "slug is required for uninstall_external action.", nil
	}

	if err := s.extMgr.Uninstall(ctx, slug); err != nil {
		return fmt.Sprintf("Uninstall failed: %v", err), nil
	}

	return fmt.Sprintf("Uninstalled skill %q.", slug), nil
}

// actionListExternal lists all installed external skills.
func (s *Skill) actionListExternal(ctx context.Context) (string, error) {
	if err := requireAdmin(ctx); err != nil {
		return err.Error(), nil
	}
	if s.extMgr == nil {
		return "External skills are not enabled.", nil
	}

	skills, err := s.extMgr.ListInstalled(ctx)
	if err != nil {
		return fmt.Sprintf("Failed to list external skills: %v", err), nil
	}

	if len(skills) == 0 {
		return "No external skills installed.", nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Installed external skills (%d):\n\n", len(skills))
	for _, sk := range skills {
		status := "enabled"
		if !sk.Enabled {
			status = "DISABLED"
		}
		fmt.Fprintf(&b, "- %s (%s) v%s [%s] [%s] from %s\n",
			sk.Name, sk.Slug, sk.Version, sk.Isolation, status, sk.Source)
		if sk.Description != "" {
			fmt.Fprintf(&b, "  %s\n", sk.Description)
		}
	}
	return b.String(), nil
}

// actionUpdateExternal re-installs an external skill to update it.
func (s *Skill) actionUpdateExternal(ctx context.Context, slug string) (string, error) {
	if err := requireAdmin(ctx); err != nil {
		return err.Error(), nil
	}
	if s.extMgr == nil {
		return "External skills are not enabled.", nil
	}
	if slug == "" {
		return "slug is required for update_external action.", nil
	}

	// Fetch current skill to get source info.
	existing, err := s.extMgr.GetInstalled(ctx, slug)
	if err != nil {
		return fmt.Sprintf("Skill %q not found: %v", slug, err), nil
	}

	source := existing.Source
	sourceRef := existing.SourceRef
	oldVersion := existing.Version

	// Uninstall first.
	if err := s.extMgr.Uninstall(ctx, slug); err != nil {
		return fmt.Sprintf("Failed to uninstall old version: %v", err), nil
	}

	// Re-install from original source.
	installed, warnings, err := s.extMgr.Install(ctx, source, sourceRef)
	if err != nil {
		return fmt.Sprintf("Uninstalled old version but failed to reinstall: %v\nManual reinstall needed: action='install_external', source='%s', ref='%s'",
			err, source, sourceRef), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Updated skill %q: v%s -> v%s\n", slug, oldVersion, installed.Version)

	if len(warnings) > 0 {
		b.WriteString("Warnings:\n")
		for _, w := range warnings {
			fmt.Fprintf(&b, "- %s\n", w)
		}
	}

	return b.String(), nil
}
