package main

import (
	"context"
	"errors"
	"io"
	"testing"

	audit "github.com/aos-ref/platform/audit"
)

// AOS-328 — SOB PRODUÇÃO, UMA CUSTÓDIA INJECTADA TEM DE SABER CONFIRMAR A DESTRUIÇÃO.
//
// Sem confirmador o fluxo DSAR sela `dsar.key_destroyed` SEM PERGUNTAR — a cadeia afirma uma
// irrecuperabilidade que ninguém verificou. O AOS-322 pôs isso no banner («NAO ARMADA, e NAO E
// CORRECTO»), e declarar não é impor: o `DEF-813` nomeia a TERCEIRA custódia — um KMS que POSSA
// falhar a destruir e não implemente a porta — como o risco que a omissão reabre.
//
// A ESCOLHA FOI RECUSAR, com escape por DECLARAÇÃO explícita. As duas custódias que existem hoje
// fazem a escolha certa; o que faltava era obrigar a escolha a ser CONSCIENTE.

// custodiaSemConfirmacao é a terceira custódia do cenário: destrói, PODE falhar, e não implementa
// a porta de confirmação. É o objecto que o `DEF-813` descreve.
type custodiaSemConfirmacao struct{ audit.KeyVault }

// custodiaComConfirmacao implementa a porta interna que o adaptador procura.
type custodiaComConfirmacao struct{ audit.KeyVault }

func (custodiaComConfirmacao) shredConfirmed(string) error { return nil }

func TestAOS328_ProducaoRecusaCustodiaInjectadaSemConfirmacao(t *testing.T) {
	cfg := tnBaseConfig()
	cfg.ProductionMode = true
	cfg.DSARVault = custodiaSemConfirmacao{audit.NewInMemoryKeyVault(nil)}

	_, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err == nil {
		t.Fatal("uma custodia injectada sem confirmacao tinha de ABORTAR sob producao")
	}
	if !errors.Is(err, ErrProductionNeedsShredConfirmation) {
		t.Errorf("err = %v, quer ErrProductionNeedsShredConfirmation", err)
	}
}

// TestAOS328_ADeclaracaoExplicitaDesarmaAGuarda — o escape é uma DECLARAÇÃO, não um silêncio.
// Quem afirma que a custódia destrói incondicionalmente assume-o por escrito, no molde de
// `AOS_TLS_EXTERNAL_TERMINATION`.
func TestAOS328_ADeclaracaoExplicitaDesarmaAGuarda(t *testing.T) {
	cfg := tnBaseConfig()
	cfg.ProductionMode = true
	cfg.DSARVault = custodiaSemConfirmacao{audit.NewInMemoryKeyVault(nil)}
	cfg.ShredDestroyUnconditional = true

	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("com a declaracao explicita o arranque tem de passar: %v", err)
	}
	_ = node.Close()
}

// TestAOS328_OVaultDeReferenciaComPoeSemDeclaracao é o CONTROLO que o AC nomeia à letra: a guarda
// NÃO pode transformar o modo de desenvolvimento numa configuração cerimoniosa.
//
// O vault de referência não é INJECTADO — o nó constrói-o quando `AOS_DSAR_VAULT_ADDR` está
// vazio —, pelo que a guarda não lhe toca. Se tocasse, todo o `aos serve` de referência passaria a
// exigir uma variável nova.
func TestAOS328_OVaultDeReferenciaCompoeSemDeclaracao(t *testing.T) {
	for _, producao := range []bool{false, true} {
		cfg := tnBaseConfig()
		cfg.ProductionMode = producao
		cfg.DSARVault = nil // NÃO injectado: é o de referência

		node, err := Bootstrap(context.Background(), cfg, io.Discard)
		if err != nil {
			t.Fatalf("producao=%v: o vault de referencia tem de compor sem declaracao: %v", producao, err)
		}
		_ = node.Close()
	}
}

// TestAOS328_ACustodiaQueSabeConfirmarCompoeSemDeclaracao é o segundo CONTROLO: uma custódia que
// implementa a porta — como o Vault Transit real — passa sem escape nenhum. Sem isto, uma guarda
// que recusasse toda a custódia injectada passaria o primeiro teste e partiria o caminho correcto.
func TestAOS328_ACustodiaQueSabeConfirmarCompoeSemDeclaracao(t *testing.T) {
	cfg := tnBaseConfig()
	cfg.ProductionMode = true
	cfg.DSARVault = custodiaComConfirmacao{audit.NewInMemoryKeyVault(nil)}

	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("uma custodia que SABE confirmar nao precisa de declaracao: %v", err)
	}
	_ = node.Close()
}

// TestAOS328_ForaDeProducaoAGuardaNaoCorre — o eixo é de produção. Fora dela, a mesma custódia
// perigosa compõe, e o banner do AOS-322 continua a ser quem o diz. É deliberado: o modo de
// referência existe para se poder experimentar sem cerimónia.
func TestAOS328_ForaDeProducaoAGuardaNaoCorre(t *testing.T) {
	cfg := tnBaseConfig()
	cfg.ProductionMode = false
	cfg.DSARVault = custodiaSemConfirmacao{audit.NewInMemoryKeyVault(nil)}

	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("fora de producao a guarda nao corre: %v", err)
	}
	_ = node.Close()
}

// TestAOS328_ADeclaracaoNaoAceitaLixo — um valor mal escrito não pode nascer uma declaração. É o
// mesmo parser estrito do opt-out de TLS: lixo aborta, não degrada para «não declarado».
func TestAOS328_ADeclaracaoNaoAceitaLixo(t *testing.T) {
	t.Setenv("AOS_DSAR_VAULT_DESTROY_UNCONDITIONAL", "talvez")
	if _, err := nodeConfigFromEnv(); err == nil {
		t.Fatal("um valor nao reconhecido tinha de abortar, nao degradar para nao-declarado")
	}
	// E os valores reconhecidos continuam a funcionar nos dois sentidos.
	for valor, quer := range map[string]bool{"1": true, "on": true, "0": false, "": false} {
		t.Setenv("AOS_DSAR_VAULT_DESTROY_UNCONDITIONAL", valor)
		cfg, err := nodeConfigFromEnv()
		if err != nil {
			t.Fatalf("valor %q: %v", valor, err)
		}
		if cfg.ShredDestroyUnconditional != quer {
			t.Errorf("valor %q => %v, quer %v", valor, cfg.ShredDestroyUnconditional, quer)
		}
	}
}

// custodiaExternaComPortaExportada é o que uma custódia de OUTRO MÓDULO consegue de facto
// implementar: a porta EXPORTADA `dsar.ShredConfirmer`.
//
// A primeira versão do adaptador só procurava a porta INTERNA, cujo método é não-exportado — e a
// revisão adversarial mediu a consequência: nenhum tipo fora deste `package main` a podia
// satisfazer, pelo que TODA a custódia externa era recusada em produção. O remédio que o banner
// prescreve — «implemente a porta na custódia» — era impossível de seguir. É a mesma classe do F1
// do AOS-333: uma mensagem a mandar o operador por um caminho que não existe.
type custodiaExternaComPortaExportada struct{ audit.KeyVault }

func (custodiaExternaComPortaExportada) ShredConfirmed(string) error { return nil }

func TestAOS328_UmaCustodiaExternaConsegueSatisfazerAPorta(t *testing.T) {
	cfg := tnBaseConfig()
	cfg.ProductionMode = true
	cfg.DSARVault = custodiaExternaComPortaExportada{audit.NewInMemoryKeyVault(nil)}

	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("uma custodia que implementa a porta EXPORTADA tem de compor sem declaracao: %v", err)
	}
	_ = node.Close()

	// E o adaptador tem de a reconhecer — senão o banner do AOS-322 diria «NAO ARMADA» sobre
	// uma custódia que sabe responder.
	if confirmadorDeShredDe(cfg.DSARVault) == nil {
		t.Error("o adaptador nao reconheceu a porta exportada")
	}
}

// TestAOS328_OLixoNaEnvNaoEErroDeProducao — o parse corre SEMPRE, também fora de produção, pelo
// que um valor mal escrito num nó de desenvolvimento não pode abortar com um sentinela de
// produção. Atribuição errada, apanhada em revisão.
func TestAOS328_OLixoNaEnvNaoEErroDeProducao(t *testing.T) {
	t.Setenv("AOS_DSAR_VAULT_DESTROY_UNCONDITIONAL", "talvez")
	_, err := nodeConfigFromEnv()
	if err == nil {
		t.Fatal("um valor nao reconhecido tinha de abortar")
	}
	if errors.Is(err, ErrProductionNeedsShredConfirmation) {
		t.Error("um erro de CONFIG nao pode ser atribuido ao eixo de PRODUCAO — manda o operador ao sitio errado")
	}
}
