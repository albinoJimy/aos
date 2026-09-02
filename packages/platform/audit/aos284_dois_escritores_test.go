package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// aos284_dois_escritores_test.go — A MEDIÇÃO que o AOS-284 manda fazer ANTES de corrigir.
//
// O ticket é explícito sobre o seu próprio ponto de partida: «o desenho parece antecipar o
// problema — a GenesisHash fala do PRIMEIRO registo de uma PARTIÇÃO — mas NÃO ESTÁ
// VERIFICADO que exista disciplina a garantir que duas réplicas nunca partilham partição.
// O trabalho começa por confirmar se o problema existe.»
//
// Estes testes confirmam-no, e são deliberadamente DESCRITIVOS: afirmam o comportamento de
// HOJE, não o pretendido. Quando o AOS-284 entregar disciplina de partição, é ESPERADO que
// falhem — e é essa falha que provará que a correcção mudou alguma coisa. Um teste que
// sobrevivesse à correcção não estaria a medir o defeito.
//
// PORQUE isto é medível sem dois processos: o que separa dois escritores não é o sistema
// operativo, é o LOCK. O `s.mu` do FileStore é in-process e o próprio ficheiro declara-o —
// «a ordem total por-partição é a ordem de entrada no lock». Dois FileStore sobre o MESMO
// caminho têm dois locks e duas vistas em memória, que é exactamente o que duas réplicas
// são uma para a outra. Medir com dois processos mediria também o agendador do SO.
//
// NOTA sobre o guard de arranque (AOS-285/286): em produção o nó RECUSA arrancar sobre um
// WORM já detido, pelo que este cenário não acontece hoje. Isso não invalida a medição — o
// AOS-284 existe precisamente para o deployment em que dois escritores são LEGÍTIMOS, e aí
// o guard deixa de ser a resposta. Ver o residual declarado no AOS-286.

func registoDeTeste(part, razao string) AuditRecord {
	return AuditRecord{
		Partition: part, Timestamp: time.Unix(1, 0).UTC(), Decision: DecisionDeny,
		Capability: "cap:fs.read", ToolID: "doc_read",
		Code: "E_SCOPE_DENIED", DeniedBy: "scope", Reason: razao,
	}
}

// TestAOS284_MEDICAO_DoisEscritoresLegitimosFORKAMACadeia confirma que o defeito existe.
//
// Dois FileStore sobre o mesmo WAL, a escrever na MESMA partição, produzem dois registos
// que reclamam o MESMO AuditSeq e o MESMO PrevHash. Não é uma cadeia com um defeito: são
// duas cadeias a dizer que são a mesma.
func TestAOS284_MEDICAO_DoisEscritoresLegitimosFORKAMACadeia(t *testing.T) {
	ctx := context.Background()
	caminho := filepath.Join(t.TempDir(), "worm.wal")
	const particao = "gov.read/run-1"

	a, err := OpenFileStore(caminho)
	if err != nil {
		t.Fatalf("abrir A: %v", err)
	}
	defer func() { _ = a.Close() }()

	if _, err := a.Append(ctx, registoDeTeste(particao, "historia-comum")); err != nil {
		t.Fatalf("append inicial: %v", err)
	}

	// B abre o MESMO ficheiro. O replay dá-lhe a mesma história — é o arranque honesto de
	// uma segunda réplica, não um atalho de teste.
	b, err := OpenFileStore(caminho)
	if err != nil {
		t.Fatalf("abrir B: %v", err)
	}
	defer func() { _ = b.Close() }()

	deA, err := a.Append(ctx, registoDeTeste(particao, "escrito-por-A"))
	if err != nil {
		t.Fatalf("append de A: %v", err)
	}
	deB, err := b.Append(ctx, registoDeTeste(particao, "escrito-por-B"))
	if err != nil {
		t.Fatalf("append de B: %v", err)
	}

	// O FORK. Se algum dia isto deixar de ser verdade, o defeito foi corrigido — e este
	// teste tem de ser reescrito, não silenciado.
	if deA.AuditSeq != deB.AuditSeq {
		t.Fatalf("MEDIÇÃO DESMENTIDA: A selou seq %d e B selou seq %d — há disciplina que eu não vi, e o AOS-284 tem de ser reavaliado antes de se lhe tocar", deA.AuditSeq, deB.AuditSeq)
	}
	if !bytes.Equal(deA.PrevHash, deB.PrevHash) {
		t.Fatalf("MEDIÇÃO DESMENTIDA: mesmo seq mas PrevHash diferentes — o encadeamento não vem da vista em memória como eu li")
	}
	if bytes.Equal(deA.EntryHash, deB.EntryHash) {
		t.Fatalf("os dois registos têm o MESMO EntryHash — seriam o mesmo facto, e o cenário do teste está errado")
	}
	t.Logf("FORK CONFIRMADO: dois registos reclamam o seq %d com o mesmo PrevHash e EntryHash diferentes", deA.AuditSeq)
}

// TestAOS284_MEDICAO_UmForkPorCORRIDAEReportadoComoADULTERACAO é a premissa do AC2, medida.
//
// O AC2 pede que a verificação distinga adulteração de elo em falta por corrida. Isto
// mostra que hoje não distingue — e não por descuido: o vocabulário de erros tem UM
// sentinela-raiz, `ErrTampered`, e o próprio `errors.go` declara que é para «permitir um
// teste único: a cadeia foi adulterada?».
//
// A consequência operacional é a que o ticket nomeia: quem lê o erro conclui ATAQUE. Manda
// chamar o segurança quando o que houve foi duas réplicas a correr ao mesmo tempo.
func TestAOS284_MEDICAO_UmForkPorCorridaEReportadoComoAdulteracao(t *testing.T) {
	ctx := context.Background()
	caminho := filepath.Join(t.TempDir(), "worm.wal")
	const particao = "gov.read/run-2"

	a, err := OpenFileStore(caminho)
	if err != nil {
		t.Fatalf("abrir A: %v", err)
	}
	if _, err := a.Append(ctx, registoDeTeste(particao, "base")); err != nil {
		t.Fatalf("append base: %v", err)
	}

	// Checkpoint assinado sobre a história COMUM — o estado de que ambas as réplicas
	// partem, e a raiz de confiança contra a qual a verificação corre.
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("gerar chave: %v", err)
	}
	assinante, err := NewSigner(priv)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	cp, err := assinante.Seal(ctx, a, particao, 1)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	pub := assinante.Public()

	b, err := OpenFileStore(caminho)
	if err != nil {
		t.Fatalf("abrir B: %v", err)
	}
	if _, err := a.Append(ctx, registoDeTeste(particao, "escrito-por-A")); err != nil {
		t.Fatalf("append de A: %v", err)
	}
	if _, err := b.Append(ctx, registoDeTeste(particao, "escrito-por-B")); err != nil {
		t.Fatalf("append de B: %v", err)
	}
	_ = a.Close()
	_ = b.Close()

	// A cadeia como fica NO DISCO — a que um verificador, ou um nó a reiniciar,
	// encontra. Nenhum dos dois processos via a do outro em memória.
	//
	// A PRIMEIRA MEDIÇÃO ESTAVA ERRADA, e a correcção importa: eu esperava que o fork
	// fosse detectado na VERIFICAÇÃO. Não é — é detectado antes, no REPLAY, e o WORM
	// fica INABRÍVEL. O efeito não é «a verificação acusa»: é O NÓ NÃO ARRANCA.
	depois, err := OpenFileStore(caminho)
	if err == nil {
		_ = depois.Close()
		t.Fatalf("MEDIÇÃO DESMENTIDA: o WAL forkado REABRIU — então o fork sobrevive em silêncio e o problema é outro (pior): uma cadeia que verifica como íntegra sem o ser")
	}
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("MEDIÇÃO DESMENTIDA: a recusa não desembrulha para ErrTampered (%v) — já existe distinção que eu não vi", err)
	}

	// O AC2, agora fechado: a recusa CLASSIFICA a causa em vez de a confundir.
	//
	// A PRIMEIRA VERSÃO DESTA ASSERÇÃO ERA FRACA, e o registo fica porque a lição é a
	// que mais custa aprender: eu afirmava `strings.Contains(err.Error(), "adultera")`.
	// Passava antes da correcção (a mensagem dizia «adulteracao insertion») E DEPOIS
	// dela (o invólucro novo diz «nao adulteracao»). Uma asserção sobre PROSA casa com
	// os dois mundos e não discrimina nenhum — parecia um teste e não era.
	//
	// A asserção certa é sobre a CLASSIFICAÇÃO, que é o que a correcção mudou.
	if !errors.Is(err, ErrChainForked) {
		t.Fatalf("a recusa devia classificar BIFURCAÇÃO e não adulteração: %v", err)
	}
	var ve *VerifyError
	if !errors.As(err, &ve) || ve.Type != TamperFork {
		t.Fatalf("o tipo devia ser TamperFork; veio %v", err)
	}
	t.Logf("CLASSIFICADO como bifurcação, no arranque: %v", err)

	// E o checkpoint assinado sobre a história comum continua válido por si — o que
	// mostra que a raiz de confiança não é o problema: o problema é o log por baixo dela.
	if err := VerifyCheckpoint(pub, cp); err != nil {
		t.Fatalf("o checkpoint da história comum devia continuar a verificar: %v", err)
	}
}

// TestAOS284_CONTROLO_ComParticoesDISTINTASNaoHaFork é a prova de NÃO-VACUIDADE das duas
// medições acima — e, ao mesmo tempo, a demonstração da forma que a correcção tem de ter.
//
// Os mesmos dois escritores, o mesmo ficheiro, os mesmos caminhos de código. A ÚNICA
// diferença é que cada um escreve na SUA partição. Não há fork, e o WAL reabre. Se as
// medições acima passassem também assim, não estariam a medir o defeito — estariam a medir
// «dois FileStore sobre um ficheiro», que é outra coisa.
//
// É por isso que o AC1 do ticket pede um ESCRITOR EXCLUSIVO POR PARTIÇÃO e não um lock
// global: a cadeia já é por-partição por construção (a GenesisHash prova-o). O que falta não
// é serializar tudo — é garantir que duas réplicas nunca partilham uma partição.
func TestAOS284_CONTROLO_ComParticoesDistintasNaoHaFork(t *testing.T) {
	ctx := context.Background()
	caminho := filepath.Join(t.TempDir(), "worm.wal")

	a, err := OpenFileStore(caminho)
	if err != nil {
		t.Fatalf("abrir A: %v", err)
	}
	b, err := OpenFileStore(caminho)
	if err != nil {
		t.Fatalf("abrir B: %v", err)
	}

	deA, err := a.Append(ctx, registoDeTeste("gov.read/replica-A", "de-A"))
	if err != nil {
		t.Fatalf("append de A: %v", err)
	}
	deB, err := b.Append(ctx, registoDeTeste("gov.read/replica-B", "de-B"))
	if err != nil {
		t.Fatalf("append de B: %v", err)
	}
	_ = a.Close()
	_ = b.Close()

	// Ambos são o primeiro da SUA cadeia, e partem de genesis DIFERENTES — é a partição
	// que entra na GenesisHash, e é isso que torna as duas cadeias independentes.
	if deA.AuditSeq != 1 || deB.AuditSeq != 1 {
		t.Fatalf("cada partição devia começar em 1; veio A=%d B=%d", deA.AuditSeq, deB.AuditSeq)
	}
	if bytes.Equal(deA.PrevHash, deB.PrevHash) {
		t.Fatalf("duas partições distintas partilharam genesis — a separação por partição não é real")
	}

	// E o WAL REABRE. É o contraste que dá sentido à medição: o mesmo ficheiro, os mesmos
	// dois escritores, e desta vez o nó arrancaria.
	depois, err := OpenFileStore(caminho)
	if err != nil {
		t.Fatalf("com partições distintas o WAL tinha de reabrir, e não reabriu: %v", err)
	}
	defer func() { _ = depois.Close() }()
}
