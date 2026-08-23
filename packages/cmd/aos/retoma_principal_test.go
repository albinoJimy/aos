package main

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------------------------
// A RETOMA NÃO É UMA VIA PARA TERCEIROS CONTINUAREM O RUN DE ALGUÉM.
//
// Achado da verificação de completude de 2026-08-23. O [ErrResumePrincipalMismatch] estava
// declarado, com docstring, e NUNCA era devolvido — `grep` em todo o repositório devolvia uma
// única ocorrência: a própria declaração. A credencial entrava em `rec.GoalWith(credential)` sem
// alguma vez ser confrontada com `rec.Principal.NHIID`.
//
// É o molde do `gate_path`: uma defesa que o código AFIRMA ter. O comentário do `Resume` remetia
// para «a verificação explícita em (4)», que não existia.
//
// A DEFESA A JUSANTE NÃO CHEGAVA: a retoma reproduz os turnos da captura sem reinterrogar o
// modelo, e nada compara o principal resolvido do token com o `Goal.Principal`, que vem do
// REGISTO. Procurei essa comparação em todo o repositório e não existe.
// ---------------------------------------------------------------------------------------------

// credencialPara cunha um token NHI válido para o agente dado, pela autoridade do próprio nó.
func credencialPara(t *testing.T, node *Node, agente string) string {
	t.Helper()
	tok, err := node.Authority.MintForHuman(context.Background(), tnHuman, agente, tnClass, []string{tnCap})
	if err != nil {
		t.Fatalf("MintForHuman(%s): %v", agente, err)
	}
	return tok.Compact
}

func TestARetomaRECUSAAcredencialDeOUTROPrincipal(t *testing.T) {
	node, svc, _, _ := aos263Node(t)
	const run = "run-principal-intruso"
	// Run suspenso e retomável, com registo cujo principal é `nhi:agente-263`.
	aos263TornaRetomavel(t, node, svc, run)

	// CONTROLO DO CENÁRIO: sem verificador composto a guarda nunca dispararia e este teste
	// passaria por vacuidade.
	if node.Verifier == nil {
		t.Fatal("verificador de identidade nao composto — a guarda nunca dispararia")
	}

	err := svc.Resume(context.Background(), run, credencialPara(t, node, "agt-intruso"))
	if !errors.Is(err, ErrResumePrincipalMismatch) {
		t.Fatalf("a retoma com credencial de OUTRO principal devia dar ErrResumePrincipalMismatch, "+
			"veio %v — qualquer portador de uma credencial NHI valida continua o run de outro "+
			"titular, e o run continua ATRIBUIDO a quem era", err)
	}
}

// TestARetomaACEITAAcredencialDoPROPRIO é a âncora anti-vacuidade.
//
// Sem ela, «recusar sempre» passaria no teste acima — e tornaria irretomável todo o run legítimo,
// que é o defeito simétrico e igualmente mau.
//
// A retoma segue e falha MAIS À FRENTE (não há capturas para reproduzir); o que se assere é que
// NÃO falha por divergência de principal, ou seja, que a guarda deixou passar.
func TestARetomaACEITAAcredencialDoPROPRIO(t *testing.T) {
	node, svc, _, _ := aos263Node(t)
	const run = "run-principal-dono"
	aos263TornaRetomavel(t, node, svc, run)

	err := svc.Resume(context.Background(), run, credencialPara(t, node, "nhi:agente-263"))
	if errors.Is(err, ErrResumePrincipalMismatch) {
		t.Errorf("a credencial do PROPRIO principal foi recusada como divergente — a guarda "+
			"tornou irretomavel um run legitimo: %v", err)
	}
}

// TestUmaCredencialQueNaoVERIFICANaoEDivergencia — o residual declarado, provado.
//
// Só se recusa a DIVERGÊNCIA. Uma credencial que não verifica de todo continua a ser negada a
// jusante, atribuivelmente — transformar «não consegui verificar» em «não és tu» diria uma coisa
// diferente da que se sabe, e trocaria um erro honesto por um veredicto inventado.
func TestUmaCredencialQueNaoVERIFICANaoEDivergencia(t *testing.T) {
	node, svc, _, _ := aos263Node(t)
	const run = "run-principal-lixo"
	aos263TornaRetomavel(t, node, svc, run)

	err := svc.Resume(context.Background(), run, "isto-nao-e-um-token")
	if errors.Is(err, ErrResumePrincipalMismatch) {
		t.Errorf("uma credencial ILEGIVEL foi rotulada como divergencia de principal — o erro "+
			"aponta para a causa errada e custa horas num incidente: %v", err)
	}
}
