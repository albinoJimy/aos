package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// A ÂNCORA EM USO NÃO É A ÂNCORA ENTREGUE — e o alerta da selagem morta depende da segunda.
//
// `aos_worm_anchor_age_seconds` lê a âncora que PASSOU no arranque. O nó lê os ficheiros montados
// UMA VEZ, no arranque, e a entrega diária substitui-os SEM reiniciar nada. Num nó que fique de
// pé, essa série cresce 24 h por dia com a tarefa de selagem perfeitamente VIVA, e o limiar
// documentado (48 h) disparava dois dias depois do último arranque — um falso positivo garantido
// numa série que existe para detectar uma morte silenciosa.
//
// Estes testes fixam a separação: EM USO (verificada, fotografia do arranque) vs. ENTREGUE
// (relida a cada recolha, NÃO verificada). E fixam os limites do que a segunda pode prometer.
// ---------------------------------------------------------------------------------------------

// escreveEntregue materializa o PAR montado — checkpoints (o array que o `aos-issuer worm-seal`
// emite) e os pisos coerentes com ele — e devolve os dois caminhos.
func escreveEntregue(t *testing.T, cps []audit.Checkpoint) (string, string) {
	t.Helper()
	pisos := map[string]uint64{}
	for _, cp := range cps {
		pisos[cp.Partition] = cp.AuditSeq
		if cp.AuditSeq == 0 {
			pisos[cp.Partition] = 1
		}
	}
	return escreveEntreguePar(t, cps, pisos)
}

// escreveEntreguePar é a versão que deixa o teste DESCASAR as duas metades — que é o modo de
// falha real da entrega (dois `mv` consecutivos).
func escreveEntreguePar(t *testing.T, cps []audit.Checkpoint, pisos map[string]uint64) (string, string) {
	t.Helper()
	dir := t.TempDir()
	caminhoCps := filepath.Join(dir, "checkpoints.json")
	caminhoPisos := filepath.Join(dir, "heads.json")
	escreveJSON(t, caminhoCps, cps)
	escreveJSON(t, caminhoPisos, pisos)
	return caminhoCps, caminhoPisos
}

func escreveJSON(t *testing.T, caminho string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caminho, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// ancoraCom monta um handler com âncora EM USO selada em `emUso` e o par montado nos caminhos
// dados (vazios ⇒ âncora injectada em processo, sem ficheiros).
func ancoraCom(t *testing.T, emUso time.Time, caminhoCps, caminhoPisos string) *apiHandler {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &apiHandler{node: &Node{WORM: audit.NewMemStore(), ancora: &WormAnchor{
		Public:            pub,
		Checkpoints:       []audit.Checkpoint{{Partition: "run-a", AuditSeq: 1, Timestamp: emUso}},
		ExpectedHeads:     map[string]uint64{"run-a": 1},
		CheckpointFile:    caminhoCps,
		ExpectedHeadsFile: caminhoPisos,
	}}, svc: &NodeService{}}
}

// TestIdadeEntregueNaoEnvelheceComOProcesso é o teste do FALSO POSITIVO, e é o que motivou tudo.
//
// Cenário: processo de pé há 30 h; a tarefa de selagem correu há 2 h e entregou. A série EM USO
// tem de continuar a dizer 30 h (é a verdade sobre o que este processo verificou) e a série
// ENTREGUE tem de dizer 2 h — senão o alerta de «selagem morta» dispara sobre uma tarefa viva.
//
// MUTAÇÃO QUE ISTO MATA: fazer a série entregue derivar de `a.Checkpoints` (a fotografia do
// arranque) em vez de reler os ficheiros. As duas idades ficariam iguais e o falso positivo
// voltaria intacto, com uma série nova a atestá-lo.
func TestIdadeEntregueNaoEnvelheceComOProcesso(t *testing.T) {
	emUso := time.Now().Add(-30 * time.Hour)
	entregue := time.Now().Add(-2 * time.Hour)
	cps, pisos := escreveEntregue(t, []audit.Checkpoint{
		{Partition: "run-a", AuditSeq: 9, Timestamp: entregue},
		{Partition: "run-b", AuditSeq: 4, Timestamp: entregue},
	})
	corpo := metricasDe(t, ancoraCom(t, emUso, cps, pisos))

	idade, ok := valorDe(t, corpo, "aos_worm_anchor_age_seconds")
	if !ok || idade < 29*3600 {
		t.Errorf("aos_worm_anchor_age_seconds = %.0f s (presente=%v), queria ~30h — a serie EM USO "+
			"mede o que ESTE PROCESSO verificou e nao pode passar a seguir o ficheiro", idade, ok)
	}
	entregueIdade, ok := valorDe(t, corpo, "aos_worm_anchor_delivered_age_seconds")
	if !ok {
		t.Fatal("aos_worm_anchor_delivered_age_seconds AUSENTE — e a unica serie que deteta a tarefa de selagem morta sem depender de restarts")
	}
	if entregueIdade > 3*3600 {
		t.Errorf("aos_worm_anchor_delivered_age_seconds = %.0f s, queria ~2h — leu a fotografia do "+
			"arranque em vez dos ficheiros MONTADOS, e o falso positivo das 48h esta de volta", entregueIdade)
	}
	if v, ok := valorDe(t, corpo, "aos_worm_anchor_delivered_unreadable"); !ok || v != 0 {
		t.Errorf("aos_worm_anchor_delivered_unreadable = %v (presente=%v), queria 0 sobre um par coerente", v, ok)
	}
}

// TestIdadeEntregueUsaOSeloMAISRECENTE fixa qual dos instantes se usa, como na série EM USO.
//
// Um ficheiro pode acumular selos de datas diferentes. O mais recente responde «quando foi a
// última vez que isto correu»; o mais antigo responde a outra pergunta e alertaria para sempre.
func TestIdadeEntregueUsaOSeloMAISRECENTE(t *testing.T) {
	cps, pisos := escreveEntregue(t, []audit.Checkpoint{
		{Partition: "antiga", AuditSeq: 1, Timestamp: time.Now().Add(-400 * time.Hour)},
		{Partition: "recente", AuditSeq: 2, Timestamp: time.Now().Add(-1 * time.Hour)},
	})
	idade, ok := valorDe(t, metricasDe(t, ancoraCom(t, time.Now().Add(-2*time.Hour), cps, pisos)), "aos_worm_anchor_delivered_age_seconds")
	if !ok {
		t.Fatal("serie ausente")
	}
	if idade > 2*3600 {
		t.Errorf("idade entregue = %.0f s — usou o selo MAIS ANTIGO, e essa leitura alertaria para sempre", idade)
	}
}

// TestParDESCASADOEAlertavel é o teste do modo de falha MAIS PROVÁVEL desta operação, e apareceu
// numa revisão adversarial — a primeira versão relia SÓ os checkpoints e dava isto por saudável.
//
// A entrega troca os dois ficheiros com dois `mv` consecutivos. Entre eles — e PERMANENTEMENTE se
// o segundo falhar — os checkpoints são novos e os pisos velhos: uma partição nova fica com
// checkpoint e SEM piso, que é exactamente o que faz o arranque abortar
// (`ErrBadWormExpectedHead`). Publicar `unreadable 0` aqui é mostrar uma tarefa de selagem viva e
// saudável sobre um nó que já não levanta.
func TestParDESCASADOEAlertavel(t *testing.T) {
	entregue := time.Now().Add(-1 * time.Hour)
	// Checkpoints NOVOS (duas partições) contra pisos VELHOS (só a primeira) — o estado exacto
	// entre os dois `mv`.
	cps, pisos := escreveEntreguePar(t,
		[]audit.Checkpoint{
			{Partition: "run-a", AuditSeq: 9, Timestamp: entregue},
			{Partition: "run-nova", AuditSeq: 3, Timestamp: entregue},
		},
		map[string]uint64{"run-a": 9},
	)
	corpo := metricasDe(t, ancoraCom(t, time.Now().Add(-10*time.Hour), cps, pisos))

	if v, ok := valorDe(t, corpo, "aos_worm_anchor_delivered_unreadable"); !ok || v != 1 {
		t.Errorf("aos_worm_anchor_delivered_unreadable = %v (presente=%v), queria 1 — o par esta "+
			"DESCASADO (particao com checkpoint e sem piso) e o proximo arranque ABORTA", v, ok)
	}
	if _, ok := valorDe(t, corpo, "aos_worm_anchor_delivered_age_seconds"); ok {
		t.Error("saiu idade entregue sobre um par que o arranque recusa — a tarefa parecia viva e o no nao levanta")
	}
}

// TestPisosIlegiveisSaoIlegiveis — a outra metade tem o mesmo estatuto da primeira.
func TestPisosIlegiveisSaoIlegiveis(t *testing.T) {
	cps, pisos := escreveEntregue(t, []audit.Checkpoint{{Partition: "run-a", AuditSeq: 2, Timestamp: time.Now()}})
	if err := os.WriteFile(pisos, []byte("{nao sou json"), 0o600); err != nil {
		t.Fatal(err)
	}
	corpo := metricasDe(t, ancoraCom(t, time.Now().Add(-3*time.Hour), cps, pisos))
	if v, ok := valorDe(t, corpo, "aos_worm_anchor_delivered_unreadable"); !ok || v != 1 {
		t.Errorf("unreadable = %v (presente=%v), queria 1 — pisos malformados abortam o arranque", v, ok)
	}
}

// TestSemCaminhoNAOSaemSeriesDeEntrega — a mesma regra das outras famílias: não emitir um número
// quando não há facto.
//
// Uma âncora INJECTADA em processo (testes, embedders) não tem ficheiros montados. Emitir
// `delivered_unreadable 0` ali afirmaria que se leu um ficheiro que ninguém leu, e emitir
// `delivered_age 0` diria «acabado de selar» sobre um nó onde nada foi entregue.
func TestSemCaminhoNAOSaemSeriesDeEntrega(t *testing.T) {
	corpo := metricasDe(t, ancoraCom(t, time.Now().Add(-5*time.Hour), "", ""))

	if _, ok := valorDe(t, corpo, "aos_worm_anchor_delivered_age_seconds"); ok {
		t.Error("sem caminho montado saiu a idade entregue — idade 0 le-se como «acabado de selar»")
	}
	if _, ok := valorDe(t, corpo, "aos_worm_anchor_delivered_unreadable"); ok {
		t.Error("sem caminho montado saiu delivered_unreadable — afirmaria uma leitura que nao aconteceu")
	}
	// CONTROLO: a série EM USO sai na mesma. É um facto sobre o que o arranque verificou.
	if _, ok := valorDe(t, corpo, "aos_worm_anchor_age_seconds"); !ok {
		t.Error("a serie EM USO desapareceu — o cenario nao foi montado")
	}
}

// TestFicheiroEntregueIlegivelEAlertavel — o ficheiro partido tem de ser VISÍVEL antes do restart.
//
// `unreadable=1` significa que o próximo arranque abortaria fail-closed. Sem esta série, a idade
// entregue desaparecia em silêncio e o operador descobria o ficheiro partido num restart que já
// não volta — que é o pior sítio para o descobrir.
//
// O caso do DIRECTÓRIO (e, pela mesma guarda, o de um FIFO) não é exotismo: `os.ReadFile` sobre um
// FIFO sem escritor NUNCA termina, e numa rota anónima recolhida de 15 em 15 segundos isso seria
// uma goroutine e um descritor presos por recolha. A guarda é recusar o que não é ficheiro
// regular ANTES de abrir.
func TestFicheiroEntregueIlegivelEAlertavel(t *testing.T) {
	casos := map[string]func(t *testing.T) (string, string){
		"JSON malformado": func(t *testing.T) (string, string) {
			cps, pisos := escreveEntregue(t, []audit.Checkpoint{{Partition: "run-a", AuditSeq: 1, Timestamp: time.Now()}})
			if err := os.WriteFile(cps, []byte("{nao sou json"), 0o600); err != nil {
				t.Fatal(err)
			}
			return cps, pisos
		},
		"array vazio": func(t *testing.T) (string, string) {
			// O arranque recusa um ficheiro sem checkpoints; a métrica tem de o prever.
			return escreveEntregue(t, []audit.Checkpoint{})
		},
		"ficheiro desaparecido": func(t *testing.T) (string, string) {
			dir := t.TempDir()
			return filepath.Join(dir, "nao-existe.json"), filepath.Join(dir, "tambem-nao.json")
		},
		"caminho e um directorio": func(t *testing.T) (string, string) {
			_, pisos := escreveEntregue(t, []audit.Checkpoint{{Partition: "run-a", AuditSeq: 1, Timestamp: time.Now()}})
			return t.TempDir(), pisos
		},
		"acima do tecto": func(t *testing.T) (string, string) {
			cps, pisos := escreveEntregue(t, []audit.Checkpoint{{Partition: "run-a", AuditSeq: 1, Timestamp: time.Now()}})
			gordo := append([]byte("["), []byte(strings.Repeat("x", maxAncoraMontada))...)
			if err := os.WriteFile(cps, gordo, 0o600); err != nil {
				t.Fatal(err)
			}
			return cps, pisos
		},
	}
	for nome, monta := range casos {
		t.Run(nome, func(t *testing.T) {
			c, p := monta(t)
			corpo := metricasDe(t, ancoraCom(t, time.Now().Add(-10*time.Hour), c, p))

			if v, ok := valorDe(t, corpo, "aos_worm_anchor_delivered_unreadable"); !ok || v != 1 {
				t.Errorf("aos_worm_anchor_delivered_unreadable = %v (presente=%v), queria 1 — o proximo arranque ABORTA e nada o anuncia", v, ok)
			}
			if _, ok := valorDe(t, corpo, "aos_worm_anchor_delivered_age_seconds"); ok {
				t.Error("saiu uma idade entregue a partir de um ficheiro que nao le — inventaria frescura")
			}
			// CONTROLO: o nó continua a servir, e a âncora EM USO continua a ser verdade. Um
			// ficheiro montado partido NÃO invalida o que o arranque verificou.
			if _, ok := valorDe(t, corpo, "aos_worm_anchor_age_seconds"); !ok {
				t.Error("a serie EM USO desapareceu por causa do ficheiro entregue — sao independentes")
			}
		})
	}
}

// TestEntregueSemCarimboNAOEmiteIdade — o ficheiro parseia mas não diz quando foi selado.
//
// `unreadable` fica a 0 (o arranque aceitaria este par) e a idade NÃO sai: um `0` leria-se
// «acabado de selar» sobre um ficheiro que não carimba nada.
func TestEntregueSemCarimboNAOEmiteIdade(t *testing.T) {
	cps, pisos := escreveEntregue(t, []audit.Checkpoint{{Partition: "run-a", AuditSeq: 3}})
	corpo := metricasDe(t, ancoraCom(t, time.Now().Add(-4*time.Hour), cps, pisos))

	if v, ok := valorDe(t, corpo, "aos_worm_anchor_delivered_unreadable"); !ok || v != 0 {
		t.Errorf("delivered_unreadable = %v (presente=%v), queria 0 — o par parseia e o arranque aceita-o", v, ok)
	}
	if _, ok := valorDe(t, corpo, "aos_worm_anchor_delivered_age_seconds"); ok {
		t.Error("saiu idade entregue sem carimbo nenhum — 0 le-se como «acabado de selar»")
	}
}

// TestCarimboDoFuturoSaiNEGATIVOENaoAparado fixa a decisão que salva o alerta, e também veio de
// revisão adversarial.
//
// O carimbo é do relógio de QUEM SELA — outra máquina — comparado com o relógio do nó. Um relógio
// adiantado dá idade NEGATIVA. Aparar para 0 seria o pior tratamento possível: `0` lê-se «acabado
// de selar», fica eternamente abaixo de 172800, e a morte da tarefa deixaria de ser detectável —
// a falha que esta série existe para fechar, na versão silenciosa.
//
// Sai negativa, que é absurdo à vista, e a regra de alerta documentada é `> 172800 OU < 0`.
func TestCarimboDoFuturoSaiNEGATIVOENaoAparado(t *testing.T) {
	cps, pisos := escreveEntregue(t, []audit.Checkpoint{
		{Partition: "run-a", AuditSeq: 4, Timestamp: time.Now().Add(3 * time.Hour)},
	})
	idade, ok := valorDe(t, metricasDe(t, ancoraCom(t, time.Now().Add(-6*time.Hour), cps, pisos)), "aos_worm_anchor_delivered_age_seconds")
	if !ok {
		t.Fatal("a idade entregue desapareceu com um carimbo do futuro — o desvio de relogio ficaria invisivel")
	}
	if idade >= 0 {
		t.Errorf("idade entregue = %.0f s com carimbo do FUTURO, queria negativa — aparar para 0 "+
			"silencia o alerta para sempre, que e a pior forma desta falha", idade)
	}
}

// TestOAmbienteLIGAOsCaminhosDaAncoraAoMetrics é o teste de CABLAGEM, e é o que impede o defeito
// de campo-fantasma que já apareceu sete vezes neste repositório.
//
// Os testes acima constroem o `WormAnchor` à mão, com os caminhos preenchidos. Uma mutação que
// removesse `CheckpointFile`/`ExpectedHeadsFile` do `parseWormAnchorFromEnv` passava em TODOS
// eles: a métrica continuaria correcta e ninguém lhe daria os caminhos. Em produção, as séries de
// entrega nunca sairiam — e o falso positivo das 48 h ficaria de pé com duas séries novas a
// fingir que não.
//
// Vai pelo AMBIENTE, que é por onde os caminhos entram no nó de verdade.
func TestOAmbienteLIGAOsCaminhosDaAncoraAoMetrics(t *testing.T) {
	entregue := time.Now().Add(-90 * time.Minute)
	cps, pisos := escreveEntregue(t, []audit.Checkpoint{{Partition: "run-a", AuditSeq: 5, Timestamp: entregue}})
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AOS_WORM_TRUST_ANCHOR", hex.EncodeToString(pub))
	t.Setenv("AOS_WORM_CHECKPOINT_FILE", cps)
	t.Setenv("AOS_WORM_EXPECTED_HEADS_FILE", pisos)
	t.Setenv("AOS_WORM_EXPECTED_HEAD", "") // a env obsoleta aborta o parse se estiver presente.

	anc, err := parseWormAnchorFromEnv()
	if err != nil {
		t.Fatalf("parseWormAnchorFromEnv: %v", err)
	}
	if anc.CheckpointFile != cps || anc.ExpectedHeadsFile != pisos {
		t.Fatalf("o parse do ambiente NAO reteve os caminhos montados: checkpoints=%q pisos=%q — "+
			"as series de entrega nunca sairiam em producao", anc.CheckpointFile, anc.ExpectedHeadsFile)
	}

	h := &apiHandler{node: &Node{WORM: audit.NewMemStore(), ancora: anc}, svc: &NodeService{}}
	idade, ok := valorDe(t, metricasDe(t, h), "aos_worm_anchor_delivered_age_seconds")
	if !ok {
		t.Fatal("a ancora vinda do AMBIENTE nao produziu a idade entregue — os caminhos nao chegaram ao /metrics")
	}
	if idade > 2*3600 {
		t.Errorf("idade entregue = %.0f s, queria ~90 min", idade)
	}
}
