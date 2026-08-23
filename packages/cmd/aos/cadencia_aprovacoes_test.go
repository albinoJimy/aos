package main

import (
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------------------
// A CADÊNCIA DO VARRIMENTO DE APROVAÇÕES DEIXA DE SER A EXCEPÇÃO SILENCIOSA.
//
// Achado da verificação de completude de 2026-08-23. Era a única cadência do ficheiro que
// descartava em SILÊNCIO um valor ilegível — as irmãs (retenção, crash-resume, SLOs, limiares do
// disjuntor) abortam o arranque.
//
// A JUSTIFICAÇÃO ESTAVA ESCRITA AO LADO e era honesta quando foi escrita: «o varrimento de
// aprovações é higiene operacional; [o de retenção] conduz a EXPIRAÇÃO POR TTL».
//
// CADUCOU COM AOS-263, e a cadeia é esta:
//
//	`Resume` recusa enquanto houver prompt de exaustão POR RESPONDER
//	   ↓
//	`ListForRun` só o deixa de devolver depois de RETIRADO
//	   ↓
//	quem o retira é `ExpireKind` — chamado APENAS pelo varredor
//
// Com `AOS_APPROVAL_SWEEP_INTERVAL=0`, que era aceite, um run com pergunta de exaustão fica
// PERMANENTEMENTE irretomável. O operador vê `waiting_on_human`, a retoma recusa com 409, e a
// causa está numa variável de ambiente que ninguém volta a ler.
// ---------------------------------------------------------------------------------------------

func TestCadenciaDeAprovacoesVAZIA_DaODefault(t *testing.T) {
	t.Setenv("AOS_APPROVAL_SWEEP_INTERVAL", "")
	d, err := approvalSweepIntervalFromEnv()
	if err != nil {
		t.Fatalf("ambiente vazio devia dar o default: %v", err)
	}
	if d != DefaultApprovalSweepInterval {
		t.Errorf("default = %s, queria %s", d, DefaultApprovalSweepInterval)
	}
}

func TestCadenciaDeAprovacoesVALIDA_EmVigor(t *testing.T) {
	t.Setenv("AOS_APPROVAL_SWEEP_INTERVAL", "30s")
	d, err := approvalSweepIntervalFromEnv()
	if err != nil {
		t.Fatalf("30s e valido: %v", err)
	}
	if d != 30*time.Second {
		t.Errorf("devolveu %s e o operador pediu 30s — o valor anunciado tem de ser o ligado", d)
	}
}

// TestUmaCadenciaILEGIVEL_ABORTA — o defeito original: era descartada em silêncio.
func TestUmaCadenciaILEGIVEL_ABORTA(t *testing.T) {
	for _, mau := range []string{"1 minuto", "60", "abc", "-5m"} {
		t.Run(mau, func(t *testing.T) {
			t.Setenv("AOS_APPROVAL_SWEEP_INTERVAL", mau)
			if _, err := approvalSweepIntervalFromEnv(); !errors.Is(err, ErrBadApprovalSweepInterval) {
				t.Errorf("%q devia abortar com ErrBadApprovalSweepInterval, veio %v — um operador "+
					"que pede uma cadencia e fica com outra nao tem como o notar", mau, err)
			}
		})
	}
}

// TestUmaCadenciaZERO_ABORTA é o caso GRAVE, e é diferente do ilegível.
//
// `0` é uma duração VÁLIDA para o `time.ParseDuration` — passava, e desligava o varredor por
// completo. Nenhum valor pode desligá-lo: dele depende a retoma de runs com prompt de exaustão.
func TestUmaCadenciaZERO_ABORTA(t *testing.T) {
	for _, zero := range []string{"0", "0s", "0ms"} {
		t.Run(zero, func(t *testing.T) {
			t.Setenv("AOS_APPROVAL_SWEEP_INTERVAL", zero)
			if _, err := approvalSweepIntervalFromEnv(); !errors.Is(err, ErrBadApprovalSweepInterval) {
				t.Errorf("%q DESLIGAVA o varredor e era aceite — um run com pergunta de exaustao "+
					"fica permanentemente irretomavel; veio %v", zero, err)
			}
		})
	}
}

// TestAOpcaoCONTINUA_ADesligarParaTestes — a fronteira é o AMBIENTE, não a opção.
//
// Sem este ramo, tornar a opção estrita partiria os testes que precisam de desligar o laço para
// serem deterministas — e alguém removeria a guarda inteira em vez de a compreender.
func TestAOpcaoCONTINUA_ADesligarParaTestes(t *testing.T) {
	var cfg nodeServiceConfig
	WithApprovalSweepInterval(0)(&cfg)
	if cfg.sweepInterval != 0 {
		t.Errorf("a OPCAO tem de continuar a aceitar 0 (os testes desligam o laco por ela); "+
			"veio %s", cfg.sweepInterval)
	}
}
