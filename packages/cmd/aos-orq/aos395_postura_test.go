package main

// AOS-395 — a linha de postura declara o MODO do audit de governação, amarrada ao estado
// composto: sem gateway não se declara nada; com gateway e sem caminho, a volatilidade é dita em
// voz alta (era esta a lacuna: os selos perdiam-se em silêncio); com caminho, diz o caminho.

import (
	"strings"
	"testing"
)

func TestAOS395_PosturaDoAudit(t *testing.T) {
	casos := []struct {
		nome     string
		gateway  bool
		caminho  string
		vazia    bool
		contidos []string
	}{
		{nome: "sem gateway nao declara", gateway: false, caminho: "", vazia: true},
		{nome: "sem gateway ignora o caminho", gateway: false, caminho: "/tmp/x.wal", vazia: true},
		{
			nome: "gateway sem caminho declara a volatilidade", gateway: true, caminho: "",
			contidos: []string{"IN-MEMORY (VOLATIL)", "PERDEM-SE", "AOS_MODEL_AUDIT_PATH"},
		},
		{
			nome: "gateway com caminho declara o duravel e o caminho", gateway: true, caminho: "/var/lib/aos/orq-model-audit.wal",
			contidos: []string{"DURAVEL", "/var/lib/aos/orq-model-audit.wal", "PROPRIO"},
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			linha := modelAuditPostureBanner(c.gateway, c.caminho)
			if c.vazia {
				if linha != "" {
					t.Fatalf("esperava nenhuma linha, veio %q", linha)
				}
				return
			}
			if linha == "" {
				t.Fatal("esperava uma linha de postura, veio vazia")
			}
			for _, quer := range c.contidos {
				if !strings.Contains(linha, quer) {
					t.Fatalf("a linha nao menciona %q: %s", quer, linha)
				}
			}
		})
	}
}
