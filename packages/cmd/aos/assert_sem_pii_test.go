package main

import (
	"strings"
	"testing"

	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// A GUARDA DE PII TEM DE SABER FALHAR.
//
// Achado da verificação de completude de 2026-08-23: o `assertNoPIIInPartition` chamava-se «sem
// PII na partição» e o corpo verificava UM ponteiro — o `PayloadRef`. É o vector principal, e por
// isso a versão anterior não era inútil; mas SETE testes tratavam-no como o seu controlo de PII,
// e conteúdo pessoal escrito noutro campo passava intacto por todos.
//
// Um deles — `aos214_sovereign_replay_test.go` — acrescentava um varrimento de bytes por conta
// própria, a que chamava «defesa em profundidade». O autor desconfiava do auxiliar, e tinha razão.
//
// ESTE FICHEIRO É O CASO NEGATIVO QUE FALTAVA. Sem ele, o varrimento por sonda seria uma guarda
// que nunca falhou: hoje nada vaza, portanto nada a exercita. Uma guarda sem caso negativo é uma
// guarda por provar — e é exactamente o que o auxiliar era.
// ---------------------------------------------------------------------------------------------

// seloLimpo é o que estes caminhos selam de facto: metadados, sem texto livre e sem payload.
func seloLimpo() audit.AuditRecord {
	return audit.AuditRecord{
		AuditSeq:   1,
		Partition:  "gov.read/run-x",
		Decision:   audit.DecisionAllow,
		Capability: "read:outcome",
		Principal:  audit.Principal{NHIID: "nhi:leitor"},
		Resource:   audit.Resource{Type: "run", Value: "run-x", Region: "eu"},
	}
}

func TestAGuardaDePIIACEITAUmSeloLimpo(t *testing.T) {
	if err := piiEmSelos([]audit.AuditRecord{seloLimpo()}, nil); err != nil {
		t.Fatalf("um selo limpo foi recusado: %v — a guarda recusa tudo e nao prova nada", err)
	}
}

func TestAGuardaDePIIRECUSAUmPayloadRef(t *testing.T) {
	r := seloLimpo()
	r.PayloadRef = &audit.PayloadRef{}
	if err := piiEmSelos([]audit.AuditRecord{r}, nil); err == nil {
		t.Error("um selo com PayloadRef passou — e o vector PRINCIPAL de PII num selo")
	}
}

// TestAGuardaDePIIRECUSATextoLivreNoReason é a acusação concreta do achado.
func TestAGuardaDePIIRECUSATextoLivreNoReason(t *testing.T) {
	r := seloLimpo()
	r.Reason = "o titular Maria Silva pediu o relatorio clinico"
	if err := piiEmSelos([]audit.AuditRecord{r}, nil); err == nil {
		t.Error("um selo com texto livre no Reason passou — era exactamente o que a versao " +
			"anterior do auxiliar deixava passar, em sete testes ao mesmo tempo")
	}
}

// TestAGuardaDePIIRECUSAUmaSondaEscondidaNumaObrigacao prova que o varrimento é sobre o registo
// SERIALIZADO e não campo a campo.
//
// É o que faz a guarda apanhar PII num campo que ainda não existe: uma obrigação nova, um
// parâmetro acrescentado por outro ticket. Verificar campo a campo obrigaria a lembrar de a
// actualizar, e ninguém se lembra.
func TestAGuardaDePIIRECUSAUmaSondaEscondidaNumaObrigacao(t *testing.T) {
	const segredo = "conteudo-sintetico-que-nao-pode-vazar"
	r := seloLimpo()
	r.Obligations = []audit.Obligation{{
		Type:   "read.board",
		Fields: []string{"board:aos"},
		Params: map[string]string{"nota": "contexto: " + segredo},
	}}
	err := piiEmSelos([]audit.AuditRecord{r}, []string{segredo})
	if err == nil {
		t.Fatal("uma sonda escondida numa obrigacao passou — o varrimento nao alcanca campos novos")
	}
	if !strings.Contains(err.Error(), "VAZA") {
		t.Errorf("o erro devia nomear o vazamento; veio %q", err)
	}
}

// TestAGuardaDePIIRECUSAUmaParticaoVAZIA é o controlo anti-vacuidade do próprio auxiliar.
//
// Sem ele, um teste que chamasse o auxiliar sobre uma partição onde nada foi selado passaria — e
// «não encontrei PII» seria indistinguível de «não olhei para nada».
func TestAGuardaDePIIRECUSAUmaParticaoVAZIA(t *testing.T) {
	if err := piiEmSelos(nil, nil); err == nil {
		t.Error("uma particao VAZIA passou — «nao encontrei PII» ficaria indistinguivel de " +
			"«nao olhei para nada»")
	}
}

// TestUmaSondaVAZIANaoCasaComTudo — uma sonda vazia casaria com qualquer registo e transformaria
// a guarda num recusa-tudo.
func TestUmaSondaVAZIANaoCasaComTudo(t *testing.T) {
	if err := piiEmSelos([]audit.AuditRecord{seloLimpo()}, []string{""}); err != nil {
		t.Errorf("uma sonda VAZIA fez a guarda recusar um selo limpo: %v", err)
	}
}
