// The model list and choosing a model.
// fetchModels loads the account's models in the background, and handleModels caches them, picks a
// default when none is chosen (GPT-6.1 Sol when offered), sends a message that was waiting on a
// model, and opens the picker (picker.go) when /model asked for it. chooseModel switches models and
// saves the choice to the store.

package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/rzhang57/loop/internal/llm"
)

func (m *Model) fetchModels() tea.Cmd {
	lister := m.deps.Models
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		models, err := lister.ListModels(ctx)
		return modelsMsg{models: models, err: err}
	}
}

// handleModels caches the model list, picks a default if none is chosen yet, sends a message that
// was waiting on that, and opens the picker if /model asked for it.
func (m *Model) handleModels(msg modelsMsg) (tea.Model, tea.Cmd) {
	requested := m.pickerRequested || m.pending != ""
	m.pickerRequested = false
	if msg.err == nil && len(msg.models) == 0 {
		msg.err = errors.New("this account has no models available")
	}
	if msg.err != nil {
		if requested || sessionEnded(msg.err) {
			m.addError(msg.err)
		}
		if sessionEnded(msg.err) {
			m.email = ""
		}
		m.pending = ""
		return m, nil
	}
	m.models = msg.models

	var save tea.Cmd
	if m.modelName == "" {
		i := slices.IndexFunc(msg.models, func(info llm.ModelInfo) bool { return info.Slug == preferredModel })
		save = m.chooseModel(msg.models[max(i, 0)])
	}
	if m.pending != "" {
		text := m.pending
		m.pending = ""
		return m, tea.Batch(save, m.startTurn(text))
	}
	if requested && !m.running {
		m.openPicker()
	}
	return m, save
}

// openPicker lists the account's models to choose from, starting at the one in use.
func (m *Model) openPicker() {
	models := m.models
	rows := make([]pickerRow, len(models))
	for i, info := range models {
		rows[i] = pickerRow{label: info.DisplayName, detail: info.Slug, current: info.Slug == m.modelName}
	}
	m.picker = &picker{
		title:    "Choose a model",
		hint:     " · /model SLUG for another",
		rows:     rows,
		selected: max(slices.IndexFunc(models, func(info llm.ModelInfo) bool { return info.Slug == m.modelName }), 0),
		choose:   func(i int) tea.Cmd { return m.chooseModel(models[i]) },
	}
}

// modelInfo returns the listed model with this slug, or one named by the slug itself, since the
// account's list can leave out models its plan still serves.
func (m *Model) modelInfo(slug string) llm.ModelInfo {
	if i := slices.IndexFunc(m.models, func(info llm.ModelInfo) bool { return info.Slug == slug }); i >= 0 {
		return m.models[i]
	}
	return llm.ModelInfo{Slug: slug, DisplayName: slug}
}

// chooseModel switches to the model for the next turn and returns a command that saves the choice.
func (m *Model) chooseModel(info llm.ModelInfo) tea.Cmd {
	m.modelName = info.Slug
	m.add(entry{kind: noticeEntry, text: "Using " + info.DisplayName + "."})
	s := m.deps.Store
	if s == nil {
		return nil
	}
	return func() tea.Msg {
		if err := s.Set(context.Background(), keyModel, info.Slug); err != nil {
			return errorMsg{err: fmt.Errorf("save model choice: %w", err)}
		}
		return nil
	}
}
