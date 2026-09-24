package main

import (
	"context"
	"errors"
	"strings"
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

// TestUmaCredencialQueNaoVERIFICANaoEDivergencia — a distinção de ERRO, que continua a valer.
//
// Transformar «não consegui verificar» em «não és tu» trocaria um erro honesto por um veredicto
// inventado, e custaria horas num incidente. Esta parte do residual antigo estava certa.
//
// # O QUE MUDOU EM AOS-433
//
// A versão anterior deste teste parava aqui, e ao parar aqui **documentava um buraco como se
// fosse desenho**: dizia que a credencial ilegível «continua a ser negada a jusante», e a
// retoma prosseguia. A defesa a jusante não a apanhava — a retoma reproduz os turnos da captura
// sem reinterrogar o modelo, logo pode nunca chegar ao hook de identidade do RM.
//
// Um teste que assere só o que NÃO acontece deixa passar tudo o resto. Agora assere as duas
// coisas: que não é rotulada como divergência, **e que é recusada**.
func TestUmaCredencialQueNaoVERIFICANaoEDivergencia(t *testing.T) {
	node, svc, _, _ := aos263Node(t)
	const run = "run-principal-lixo"
	aos263TornaRetomavel(t, node, svc, run)

	err := svc.Resume(context.Background(), run, "isto-nao-e-um-token")
	if errors.Is(err, ErrResumePrincipalMismatch) {
		t.Errorf("uma credencial ILEGIVEL foi rotulada como divergencia de principal — o erro "+
			"aponta para a causa errada e custa horas num incidente: %v", err)
	}
	if !errors.Is(err, ErrResumeCredencialNaoVerifica) {
		t.Fatalf("a retoma ACEITOU uma credencial que nao verifica: %v\n"+
			"um token expirado ou REVOGADO re-hospeda o run, toma lease e consome plano de "+
			"replay — e a defesa a jusante nao o apanha, porque a retoma reproduz os turnos "+
			"sem reinterrogar o modelo", err)
	}
}

// TestAOS433ARetomaRecusaCadaCausaDeCredencialInvalida cobre as causas, não só uma.
//
// A versão antiga usava um literal ilegível («isto-nao-e-um-token»), que é a causa mais fácil de
// apanhar. Um token bem formado mas de emissor desconhecido, ou expirado, passa por caminhos
// diferentes do verificador — e é esse que um atacante teria.
func TestAOS433ARetomaRecusaCadaCausaDeCredencialInvalida(t *testing.T) {
	node, svc, _, _ := aos263Node(t)

	for _, c := range []struct{ nome, cred string }{
		{"malformada", "isto-nao-e-um-token"},
		{"tres segmentos que nao sao um JWS", "aaa.bbb.ccc"},
		{"cabecalho plausivel, resto lixo", "eyJhbGciOiJFZERTQSJ9.LS0.LS0"},
	} {
		t.Run(c.nome, func(t *testing.T) {
			run := "run-433-" + strings.ReplaceAll(c.nome, " ", "-")
			aos263TornaRetomavel(t, node, svc, run)
			err := svc.Resume(context.Background(), run, c.cred)
			if !errors.Is(err, ErrResumeCredencialNaoVerifica) {
				t.Errorf("a retoma aceitou uma credencial %s: %v", c.nome, err)
			}
		})
	}
}

// TestAOS433ARetomaLEGITIMAContinuaAFuncionar é o controlo de não-vacuidade.
//
// Sem ele, a guarda podia recusar TUDO e os testes acima continuariam verdes — que é a forma
// mais fácil de «fechar» um buraco de segurança sem dar por isso.
func TestAOS433ARetomaLEGITIMAContinuaAFuncionar(t *testing.T) {
	node, svc, _, _ := aos263Node(t)
	const run = "run-433-legitimo"
	aos263TornaRetomavel(t, node, svc, run)

	// O principal do registo é `nhi:agente-263` (ver `aos263TornaRetomavel`), logo esta é a
	// credencial do DONO: passa a guarda nova E a comparação de principal que vem a seguir.
	err := svc.Resume(context.Background(), run, credencialPara(t, node, "nhi:agente-263"))
	if errors.Is(err, ErrResumeCredencialNaoVerifica) {
		t.Fatalf("a guarda recusou uma credencial VALIDA — tornou a retoma inutilizavel: %v", err)
	}
}
