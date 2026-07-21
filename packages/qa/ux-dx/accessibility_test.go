package uxdx_test

import (
	"testing"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	progresssurface "github.com/aos-ref/control-plane/governance/progress-surface"
	surfaceadapter "github.com/aos-ref/control-plane/governance/surface-adapter"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// AC4 — ACESSIBILIDADE BÁSICA. No reference model offline, a acessibilidade é sobre a
// ESTRUTURA renderizada carregar os metadados necessários: rótulos NÃO-VAZIOS, acções
// NOMEADAS/identificáveis, preview apresentável. A bateria assere que as superfícies
// EXPÕEM esses metadados (não é um teste de UI real).

// As renders das 3 plataformas expõem rótulos não-vazios, acções nomeadas e o preview
// apresentável — os metadados que uma camada de acessibilidade (contraste/rótulos/
// navegação por teclado) precisa de consumir.
func TestAccessibility_SurfacesExposeLabelsAndNamedActions(t *testing.T) {
	t.Parallel()

	card, err := approvalcard.BuildCard(
		risk.ConfirmationRequest{
			Class:      risk.ClassGray,
			Preview:    "cap:http.get -> https://api/synthetic",
			Principal:  requesterID,
			Capability: "cap:http.get",
			Resource:   "https://api/synthetic",
		},
		approvalcard.WithRequestID("a11y-card-1"),
	)
	if err != nil {
		t.Fatalf("BuildCard: %v", err)
	}

	for _, p := range allPlatforms {
		r, _ := surfaceadapter.RendererFor(p)
		rc, err := r.Render(card)
		if err != nil {
			t.Fatalf("Render(%s): %v", p, err)
		}
		// Preview apresentável (o efeito concreto legível pelo utilizador).
		if rc.Preview == "" {
			t.Fatalf("plataforma %s: preview vazio (nada a apresentar)", p)
		}
		// Cada acção é NOMEADA (Kind identificável) e ROTULADA (Label não-vazio).
		if len(rc.Actions) == 0 {
			t.Fatalf("plataforma %s: sem acções (nada navegável)", p)
		}
		for _, a := range rc.Actions {
			if a.Kind == "" {
				t.Fatalf("plataforma %s: acção sem Kind (não identificável)", p)
			}
			if a.Label == "" {
				t.Fatalf("plataforma %s: acção %q sem rótulo (não apresentável/legível)", p, a.Kind)
			}
		}
		// Os controlos específicos da plataforma carregam texto/rótulo (contraste/leitor
		// de ecrã precisam de texto, não só de ícones).
		assertPlatformLabels(t, rc)
	}
}

// As opções do prompt de exaustão são NOMEADAS e distintas — navegáveis e legíveis, sem
// depender de ordem visual apenas.
func TestAccessibility_ExhaustionOptionsAreNamed(t *testing.T) {
	t.Parallel()
	for _, o := range progresssurface.PromptOptions() {
		if o.String() == "" || o.String() == "unset" {
			t.Fatalf("opção de exaustão sem rótulo legível: %v", o)
		}
	}
}

// assertPlatformLabels verifica que os controlos específicos da plataforma carregam
// texto/rótulos não-vazios (o mínimo de acessibilidade estrutural no reference model).
func assertPlatformLabels(t *testing.T, rc surfaceadapter.RenderedCard) {
	t.Helper()
	switch rc.Platform {
	case surfaceadapter.PlatformSlack:
		var hasButton bool
		for _, b := range rc.SlackBlocks {
			if b.Type == "section" && b.Text == "" {
				t.Fatal("Slack: bloco section sem texto (nada a ler)")
			}
			for _, e := range b.Elements {
				if e.Text == "" {
					t.Fatal("Slack: botão sem texto (não legível)")
				}
				hasButton = true
			}
		}
		if !hasButton {
			t.Fatal("Slack: nenhum botão rotulado")
		}
	case surfaceadapter.PlatformTelegram:
		if rc.TelegramKeyboard == nil || rc.TelegramKeyboard.Text == "" {
			t.Fatal("Telegram: teclado sem corpo de texto")
		}
		for _, row := range rc.TelegramKeyboard.InlineButtons {
			for _, b := range row {
				if b.Text == "" {
					t.Fatal("Telegram: botão inline sem texto")
				}
			}
		}
	case surfaceadapter.PlatformDesktop:
		if rc.DesktopComponent == nil || rc.DesktopComponent.Title == "" || rc.DesktopComponent.Body == "" {
			t.Fatal("Desktop: componente sem título/corpo")
		}
		for _, b := range rc.DesktopComponent.Buttons {
			if b.Label == "" {
				t.Fatal("Desktop: botão sem rótulo")
			}
		}
	}
}
