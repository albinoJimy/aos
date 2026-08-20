package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// UM BOM NÃO PODE IMPEDIR O NÓ DE ARRANCAR COM A ÂNCORA LIGADA.
//
// Não é um caso hipotético: aconteceu. O `selar-worm.ps1` escrevia com `Set-Content -Encoding
// utf8`, que em PowerShell 5.1 põe BOM, e a SEGUNDA selagem não conseguiu ler o ficheiro que a
// PRIMEIRA tinha escrito («invalid character 'ï'»). O nó lê os MESMOS ficheiros, e teria abortado
// em [ErrBadWormCheckpoint] — postura certa, diagnóstico que ninguém liga a codificação.
//
// O teste vai pelo `parseWormAnchorFromEnv`, e não por `parseCheckpoints`, de propósito: são DOIS
// ficheiros e só um deles passava pela função que corrigi primeiro. Testar a unidade teria dado
// verde com o ficheiro de pisos ainda partido.
// ---------------------------------------------------------------------------------------------

func comBOM(b []byte) []byte { return append([]byte{0xEF, 0xBB, 0xBF}, b...) }

func TestAncoraCarregaComBOMNosDOISFicheiros(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	cp := audit.Checkpoint{Partition: "run-a", AuditSeq: 2, EntryHash: []byte("hash")}
	cp.Signature = ed25519.Sign(priv, nil) // a FORMA é o que se parseia aqui; a assinatura é
	// verificada mais tarde, com o store composto.
	cpJSON, err := json.Marshal([]audit.Checkpoint{cp})
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	cpFile := filepath.Join(dir, "cp.json")
	headsFile := filepath.Join(dir, "heads.json")
	if err := os.WriteFile(cpFile, comBOM(cpJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(headsFile, comBOM([]byte(fmt.Sprintf("{%q: 2}", cp.Partition))), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AOS_WORM_EXPECTED_HEAD", "")
	t.Setenv("AOS_WORM_TRUST_ANCHOR", hex.EncodeToString(pub))
	t.Setenv("AOS_WORM_CHECKPOINT_FILE", cpFile)
	t.Setenv("AOS_WORM_EXPECTED_HEADS_FILE", headsFile)

	got, err := parseWormAnchorFromEnv()
	if err != nil {
		t.Fatalf("o BOM impediu a ancora de carregar: %v\n"+
			"E o caminho REAL de um operador que sele em Windows — onde as chaves vivem — e o no "+
			"abortaria no arranque com um byte invalido em vez de um diagnostico", err)
	}
	if got == nil || len(got.Checkpoints) != 1 || got.ExpectedHeads[cp.Partition] != 2 {
		t.Fatalf("ancora carregou incompleta: %+v", got)
	}
}

// TestSemBOMContinuaACarregar é o CONTROLO trivial mas obrigatório: a normalização não pode ter
// partido o caso normal, que é o que 100% dos ficheiros do Linux têm.
func TestSemBOMContinuaACarregar(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	cp := audit.Checkpoint{Partition: "run-a", AuditSeq: 2, EntryHash: []byte("hash")}
	cpJSON, _ := json.Marshal([]audit.Checkpoint{cp})

	dir := t.TempDir()
	cpFile := filepath.Join(dir, "cp.json")
	headsFile := filepath.Join(dir, "heads.json")
	_ = os.WriteFile(cpFile, cpJSON, 0o600)
	_ = os.WriteFile(headsFile, []byte(fmt.Sprintf("{%q: 2}", cp.Partition)), 0o600)

	t.Setenv("AOS_WORM_EXPECTED_HEAD", "")
	t.Setenv("AOS_WORM_TRUST_ANCHOR", hex.EncodeToString(pub))
	t.Setenv("AOS_WORM_CHECKPOINT_FILE", cpFile)
	t.Setenv("AOS_WORM_EXPECTED_HEADS_FILE", headsFile)

	if _, err := parseWormAnchorFromEnv(); err != nil {
		t.Fatalf("um ficheiro SEM BOM deixou de carregar: %v", err)
	}
}

// TestLixoContinuaARecusar é o controlo que impede a normalização de virar tolerância.
//
// Sem ele, alguém podia «resolver» o BOM aparando tudo o que não fosse `{` ou `[` — e um ficheiro
// corrompido passaria a carregar meia âncora. O que se retira é UM prefixo conhecido e mais nada.
func TestLixoContinuaARecusar(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	dir := t.TempDir()
	cpFile := filepath.Join(dir, "cp.json")
	headsFile := filepath.Join(dir, "heads.json")
	// LIXO SEGUIDO DE JSON VALIDO, e o payload importa. A primeira versao deste teste usava
	// «isto nao e json», sem `[` nem `{` — e uma mutacao que aparasse tudo ate ao primeiro deles
	// passava por ele sem tocar em nada, porque nao havia nada para aparar. Nao-vacuoso so com um
	// prefixo que uma normalizacao gulosa REMOVERIA para revelar JSON legitimo por baixo.
	cpValido, _ := json.Marshal([]audit.Checkpoint{{Partition: "run-a", AuditSeq: 2, EntryHash: []byte("h")}})
	_ = os.WriteFile(cpFile, append([]byte("LIXO ANTES DO JSON "), cpValido...), 0o600)
	_ = os.WriteFile(headsFile, []byte(`{"run-a": 2}`), 0o600)

	t.Setenv("AOS_WORM_EXPECTED_HEAD", "")
	t.Setenv("AOS_WORM_TRUST_ANCHOR", hex.EncodeToString(pub))
	t.Setenv("AOS_WORM_CHECKPOINT_FILE", cpFile)
	t.Setenv("AOS_WORM_EXPECTED_HEADS_FILE", headsFile)

	if _, err := parseWormAnchorFromEnv(); err == nil {
		t.Fatal("um checkpoint que e LIXO carregou — a normalizacao do BOM virou tolerancia, e " +
			"um ficheiro corrompido passa a poder ancorar")
	}
}

// ---------------------------------------------------------------------------------------------
// A SEED DO OPERADOR TAMBÉM NASCE EM WINDOWS.
//
// O nó tem o seu próprio carregador ([loadOperatorKey]) — módulo diferente do `aos-issuer`, e
// portanto uma terceira cópia do mesmo padrão. Sem esta ligação, `aos approve --key ...` recusaria
// uma seed perfeitamente boa escrita com `>` no PowerShell, e devolveria [ErrBadOperatorKey], que
// é indistinguível de «a chave está errada».
//
// Aqui NÃO se separa o diagnóstico de UTF-16 como no emissor: este caminho devolve
// [ErrBadOperatorKey] uniformemente por desenho. O ganho de diagnóstico fica do lado onde as
// chaves são GERADAS, que é onde o engano acontece.
// ---------------------------------------------------------------------------------------------

func TestSeedDoOperadorComBOMCarrega(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	h := hex.EncodeToString(seed)

	p := filepath.Join(t.TempDir(), "operator.seed")
	if err := os.WriteFile(p, append([]byte{0xEF, 0xBB, 0xBF}, []byte(h+"\r\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	priv, err := loadOperatorKey(p)
	if err != nil {
		t.Fatalf("a seed do operador com BOM nao carregou: %v — `aos approve` recusaria uma "+
			"chave boa com ErrBadOperatorKey, indistinguivel de «a chave esta errada»", err)
	}
	if !priv.Public().(ed25519.PublicKey).Equal(ed25519.NewKeyFromSeed(seed).Public()) {
		t.Error("carregou a chave ERRADA — a limpeza mexeu no material")
	}
}

// TestSeedDoOperadorINVALIDAContinuaARecusar é o controlo.
func TestSeedDoOperadorINVALIDAContinuaARecusar(t *testing.T) {
	for nome, conteudo := range map[string][]byte{
		"lixo":     []byte("nao e uma seed"),
		"curta":    []byte("aabbcc"),
		"so_o_bom": {0xEF, 0xBB, 0xBF},
		"com_lixo": append([]byte{0xEF, 0xBB, 0xBF}, []byte("LIXO aabb")...),
	} {
		p := filepath.Join(t.TempDir(), nome)
		if err := os.WriteFile(p, conteudo, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadOperatorKey(p); err == nil {
			t.Errorf("%s foi aceite como seed do operador", nome)
		}
	}
}
