package main

// AOS-441 — o snapshot pinado deixa de ser confiado às cegas: confere-se com o catálogo de tools
// do nó (`GET /tools`) no arranque do `consume` e do `serve`, e um que diverge é RECUSADO com a
// divergência nomeada.
//
// O caso para que o ticket existe, medido em produção: o snapshot nomeava `fs.read`, o nó chamava
// à tool `doc_read`, e cada nó do plano ficava sem nenhuma tool utilizável — fail-closed, mas sem
// fazer nada, e sem ninguém reparar durante semanas.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
)

// aos441CatalogoDoSnapshotComPerigo é o catálogo de um nó que tem EXACTAMENTE as tools de
// [aos408SnapshotComPerigo]. A versão do `http.post` é 1.0.0 e a do snapshot 2.0.0 de propósito:
// o manifesto do nó não versiona tools, e a versão não entra na comparação.
const aos441CatalogoDoSnapshotComPerigo = `{"tools":[
  {"name":"fs.read","version":"1.0.0","digest":"sha256:aaa","egress":"none","reversibility":"reversible"},
  {"name":"http.post","version":"1.0.0","digest":"sha256:bbb","egress":"external","reversibility":"irreversible"}
]}`

// aos441CatalogoRenomeado é o catálogo de PRODUÇÃO na forma que o nó lhe dá: a tool de leitura
// chama-se `doc_read`. O resto é igual — só o nome mudou.
const aos441CatalogoRenomeado = `{"tools":[
  {"name":"doc_read","version":"1.0.0","digest":"sha256:aaa","egress":"none","reversibility":"reversible"},
  {"name":"http.post","version":"1.0.0","digest":"sha256:bbb","egress":"external","reversibility":"irreversible"}
]}`

func aos441Snapshot(t *testing.T) planvalidate.Snapshot {
	t.Helper()
	snap, err := carregarSnapshot(escreverTmp(t, aos408SnapshotComPerigo))
	if err != nil {
		t.Fatalf("carregarSnapshot: %v", err)
	}
	return snap
}

func aos441Catalogo() []toolDoNo {
	return []toolDoNo{
		{Name: "fs.read", Version: "1.0.0", Digest: "sha256:aaa", Egress: "none", Reversibility: "reversible"},
		{Name: "http.post", Version: "1.0.0", Digest: "sha256:bbb", Egress: "external", Reversibility: "irreversible"},
	}
}

// ── A comparação ──────────────────────────────────────────────────────────────────────────────

func TestAOS441SnapshotQueBateComONoPassa(t *testing.T) {
	if err := compararSnapshotComCatalogo(aos441Snapshot(t), aos441Catalogo()); err != nil {
		t.Fatalf("um snapshot igual ao catálogo do nó tinha de passar: %v", err)
	}
}

// TestAOS441RenomearUmaToolNoCatalogoAvermelha é o critério do ticket, com o caso de produção.
func TestAOS441RenomearUmaToolNoCatalogoAvermelha(t *testing.T) {
	cat := aos441Catalogo()
	cat[0].Name = "doc_read"
	err := compararSnapshotComCatalogo(aos441Snapshot(t), cat)
	if !errors.Is(err, ErrSnapshotDivergeDoNo) {
		t.Fatalf("renomear `fs.read` para `doc_read` no nó tinha de avermelhar, veio %v", err)
	}
	// A divergência tem de vir NOMEADA: a tool que falta e as que o nó tem, para o operador
	// corrigir o snapshot sem ir ler o manifesto do nó.
	for _, quer := range []string{`"fs.read" não existe no nó`, "doc_read sha256:aaa", "http.post sha256:bbb"} {
		if !strings.Contains(err.Error(), quer) {
			t.Errorf("a recusa tinha de conter %q, veio: %v", quer, err)
		}
	}
}

func TestAOS441DigestQueNaoEODoNoAvermelha(t *testing.T) {
	cat := aos441Catalogo()
	cat[0].Digest = "sha256:" + strings.Repeat("d", 64)
	err := compararSnapshotComCatalogo(aos441Snapshot(t), cat)
	if !errors.Is(err, ErrSnapshotDivergeDoNo) || !strings.Contains(err.Error(), `digest do snapshot "sha256:aaa"`) ||
		!strings.Contains(err.Error(), cat[0].Digest) {
		t.Fatalf("um digest que o nó não tem tinha de avermelhar, nomeando os dois: %v", err)
	}
}

// O snapshot não pode declarar MENOS risco do que o nó — é dele que sai a classe que decide a
// aprovação automática.
func TestAOS441SnapshotMenosArriscadoDoQueONoAvermelha(t *testing.T) {
	casos := map[string]func(c []toolDoNo){
		"egress":        func(c []toolDoNo) { c[0].Egress = "external" },
		"reversibility": func(c []toolDoNo) { c[0].Reversibility = "irreversible" },
	}
	for nome, mudar := range casos {
		t.Run(nome, func(t *testing.T) {
			cat := aos441Catalogo()
			mudar(cat)
			err := compararSnapshotComCatalogo(aos441Snapshot(t), cat)
			if !errors.Is(err, ErrSnapshotDivergeDoNo) || !strings.Contains(err.Error(), "menos risco") {
				t.Fatalf("o nó declara mais risco em %s do que o snapshot, e passou: %v", nome, err)
			}
		})
	}
}

// Mais conservador do que o nó é legítimo: o `http.post` do snapshot já é externo e irreversível,
// e um nó que o declare local e reversível não torna o snapshot errado.
func TestAOS441SnapshotMaisConservadorPassa(t *testing.T) {
	cat := aos441Catalogo()
	cat[1].Egress = "none"
	cat[1].Reversibility = "reversible"
	if err := compararSnapshotComCatalogo(aos441Snapshot(t), cat); err != nil {
		t.Fatalf("um snapshot mais conservador do que o nó tinha de passar: %v", err)
	}
}

// Um eixo que este aos-orq não reconhece é divergência, não um default.
func TestAOS441EixoDoNoDesconhecidoAvermelha(t *testing.T) {
	cat := aos441Catalogo()
	cat[0].Egress = "lá-fora"
	cat[1].Reversibility = ""
	err := compararSnapshotComCatalogo(aos441Snapshot(t), cat)
	if !errors.Is(err, ErrSnapshotDivergeDoNo) || !strings.Contains(err.Error(), "2 divergência(s)") {
		t.Fatalf("dois eixos ilegíveis tinham de dar duas divergências: %v", err)
	}
}

// Um egress que o manifesto do nó não declara chega `unknown` (fail-closed, AOS-441) e conta como
// o pior caso: um snapshot que declare `none` para essa tool está a declarar menos risco.
func TestAOS441EgressUnknownDoNoRecusaSnapshotNone(t *testing.T) {
	cat := aos441Catalogo()
	cat[0].Egress = "unknown" // fs.read, que o snapshot declara `none`
	err := compararSnapshotComCatalogo(aos441Snapshot(t), cat)
	if !errors.Is(err, ErrSnapshotDivergeDoNo) || !strings.Contains(err.Error(), `o snapshot declara egress "none" e o nó "unknown"`) {
		t.Fatalf("egress unknown no nó com `none` no snapshot tinha de recusar: %v", err)
	}
	// CONTROLO: `unknown` é um valor legítimo do nó — o `http.post`, externo no snapshot, passa.
	cat = aos441Catalogo()
	cat[1].Egress = "unknown"
	if err := compararSnapshotComCatalogo(aos441Snapshot(t), cat); err != nil {
		t.Fatalf("um snapshot `external` para um egress `unknown` no nó tinha de passar: %v", err)
	}
}

// Um catálogo com o mesmo nome duas vezes não tem leitura segura: recusa nomeada, e não «o último
// ganha». Os dois contratos aqui são iguais ao do snapshot, pelo que só o duplicado pode recusar.
func TestAOS441CatalogoComNomeRepetidoRecusa(t *testing.T) {
	cat := append(aos441Catalogo(), aos441Catalogo()[0])
	err := compararSnapshotComCatalogo(aos441Snapshot(t), cat)
	if !errors.Is(err, ErrCatalogoDoNoIlegivel) || !strings.Contains(err.Error(), `a tool "fs.read" aparece mais de uma vez`) {
		t.Fatalf("um catálogo com `fs.read` repetida tinha de recusar, nomeando-a: %v", err)
	}
}

// O snapshot que o `serve` usa é o CONFERIDO, e não uma segunda leitura do ficheiro (TOCTOU).
func TestAOS441OServeUsaOSnapshotConferido(t *testing.T) {
	conferido := snapshotConferido{snap: aos441Snapshot(t), ok: true}
	// O caminho não existe: se `obter` relesse o ficheiro, falhava.
	snap, err := conferido.obter(filepath.Join(t.TempDir(), "trocado-depois-de-conferido.json"))
	if err != nil || snap.Hash != "sha256:snap-aos408" || len(snap.Tools) != 2 {
		t.Fatalf("com conferência, o serve tinha de usar o snapshot conferido: snap=%+v err=%v", snap, err)
	}
	// Sem conferência (sem executor de nós), lê o ficheiro como antes.
	if _, err := (snapshotConferido{}).obter(filepath.Join(t.TempDir(), "nao-existe.json")); err == nil {
		t.Fatal("sem conferência, o serve tinha de ler o ficheiro — e um ficheiro inexistente é erro")
	}
}

func TestAOS441NoSemToolsAvermelhaEDizPorque(t *testing.T) {
	err := compararSnapshotComCatalogo(aos441Snapshot(t), nil)
	if !errors.Is(err, ErrSnapshotDivergeDoNo) || !strings.Contains(err.Error(), "o nó não oferece tools ao modelo") {
		t.Fatalf("um nó sem tools tinha de recusar o snapshot, dizendo porquê: %v", err)
	}
}

// ── O nó falso ────────────────────────────────────────────────────────────────────────────────

// aos441No é um nó `aos` falso com o catálogo, a fila e o IdP. Conta o que o `aos-orq` lhe pede.
type aos441No struct {
	mu         sync.Mutex
	catalogo   string // corpo de GET /tools; "" ⇒ 404 (nó anterior ao AOS-441)
	reclamados int
	auth       []string // Authorization de cada GET /tools
}

func (f *aos441No) servidor(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	emitidos := 0
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		emitidos++
		n := emitidos
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"access_token":"tok-` + strings.Repeat("x", n) + `"}`))
	})
	mux.HandleFunc("GET /tools", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		corpo := f.catalogo
		f.mu.Unlock()
		if corpo == "" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(corpo))
	})
	mux.HandleFunc("POST /plans/claim", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.reclamados++
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent) // fila vazia
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *aos441No) contagem() (reclamados int, auth []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reclamados, append([]string(nil), f.auth...)
}

// ── O arranque do `consume` ───────────────────────────────────────────────────────────────────

// TestAOS441ConsumeRecusaSnapshotDivergenteAntesDeReclamar — o critério «RECUSADO no arranque do
// consume». Nenhum pedido é reclamado: reclamar e falhar a seguir gastava uma geração por nada, e
// falharia TODOS os pedidos da mesma maneira.
func TestAOS441ConsumeRecusaSnapshotDivergenteAntesDeReclamar(t *testing.T) {
	f := &aos441No{catalogo: aos441CatalogoRenomeado}
	srv := f.servidor(t)
	aos413ClienteDoAmbiente(t, srv.URL)
	snap := escreverTmp(t, aos408SnapshotComPerigo)

	err := cmdConsume([]string{"--snapshot", snap, "--wal", filepath.Join(t.TempDir(), "consume.wal")})
	if !errors.Is(err, ErrSnapshotDivergeDoNo) || !strings.Contains(err.Error(), `"fs.read" não existe no nó`) {
		t.Fatalf("o consume tinha de recusar o snapshot com a divergência nomeada, veio: %v", err)
	}
	reclamados, auth := f.contagem()
	if reclamados != 0 {
		t.Fatalf("com o snapshot divergente o consume reclamou %d pedido(s)", reclamados)
	}
	// O catálogo lê-se pelo MESMO canal autenticado das rotas irmãs.
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer tok-") {
		t.Fatalf("GET /tools tinha de levar o Bearer do IdP, levou %v", auth)
	}
}

func TestAOS441ConsumeComSnapshotConferidoReclama(t *testing.T) {
	f := &aos441No{catalogo: aos441CatalogoDoSnapshotComPerigo}
	srv := f.servidor(t)
	aos413ClienteDoAmbiente(t, srv.URL)
	snap := escreverTmp(t, aos408SnapshotComPerigo)

	if err := cmdConsume([]string{"--snapshot", snap, "--wal", filepath.Join(t.TempDir(), "consume.wal")}); err != nil {
		t.Fatalf("com o snapshot conferido o consume tinha de drenar (fila vazia), veio: %v", err)
	}
	if reclamados, _ := f.contagem(); reclamados != 1 {
		t.Fatalf("com o snapshot conferido o consume tinha de pedir à fila uma vez, pediu %d", reclamados)
	}
}

// Um consume sem --snapshot falharia cada pedido no `serve --goal`: recusa antes de reclamar.
func TestAOS441ConsumeSemSnapshotRecusaAntesDeReclamar(t *testing.T) {
	f := &aos441No{catalogo: aos441CatalogoDoSnapshotComPerigo}
	srv := f.servidor(t)
	aos413ClienteDoAmbiente(t, srv.URL)

	err := cmdConsume([]string{"--wal", filepath.Join(t.TempDir(), "consume.wal")})
	if err == nil || !strings.Contains(err.Error(), "--snapshot") {
		t.Fatalf("o consume sem --snapshot tinha de recusar, veio: %v", err)
	}
	if reclamados, _ := f.contagem(); reclamados != 0 {
		t.Fatalf("sem snapshot o consume reclamou %d pedido(s)", reclamados)
	}
}

// Um nó anterior ao AOS-441 não tem com que comparar: fail-closed, e diz porquê.
func TestAOS441NoSemCatalogoRecusa(t *testing.T) {
	f := &aos441No{} // GET /tools ⇒ 404
	srv := f.servidor(t)
	aos413ClienteDoAmbiente(t, srv.URL)
	snap := escreverTmp(t, aos408SnapshotComPerigo)

	err := cmdConsume([]string{"--snapshot", snap, "--wal", filepath.Join(t.TempDir(), "consume.wal")})
	if !errors.Is(err, ErrCatalogoDoNoIlegivel) || !strings.Contains(err.Error(), "AOS-441") {
		t.Fatalf("sem catálogo no nó o consume tinha de recusar, veio: %v", err)
	}
	// Não houve comparação: dizer que o snapshot diverge mandaria corrigir o que pode estar certo.
	if errors.Is(err, ErrSnapshotDivergeDoNo) {
		t.Fatalf("sem catálogo não se afirma divergência: %v", err)
	}
	if reclamados, _ := f.contagem(); reclamados != 0 {
		t.Fatalf("sem catálogo o consume reclamou %d pedido(s)", reclamados)
	}
}

// ── O arranque do `serve` ─────────────────────────────────────────────────────────────────────

// TestAOS441ServeConfereOSnapshotAntesDaPosse — o critério «RECUSADO no arranque do serve», pelo
// binário real (compilado UMA vez para os dois casos): com o nó renomeado sai com erro, nomeia a
// tool e não chega a abrir o WAL nem a tomar posse; com o catálogo certo — o CONTROLO, sem o qual
// a recusa podia vir de outra coisa qualquer do arranque — declara a conferência e toma posse.
func TestAOS441ServeConfereOSnapshotAntesDaPosse(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	cred := filepath.Join(dir, "nhi.jwt")
	escrever(t, cred, "nhi-do-operador")
	snap := filepath.Join(dir, "snap.json")
	escrever(t, snap, aos408SnapshotComPerigo)
	envDoNo := func(url string) []string {
		return []string{"AOS_ORQ_NODE_URL=" + url, "AOS_ORQ_NODE_CREDENTIAL_FILE=" + cred, "AOS_MODE=",
			"AOS_ORQ_OIDC_TOKEN_URL=", "AOS_ORQ_OIDC_CLIENT_ID=", "AOS_ORQ_OIDC_CLIENT_SECRET_FILE="}
	}
	// O fixture de decomposição não existe: num `serve` que passe a conferência, falha DEPOIS da
	// posse — que é exactamente a fronteira que se mede.
	fixture := filepath.Join(dir, "nao-existe.json")

	t.Run("divergente recusa antes da posse", func(t *testing.T) {
		srv := (&aos441No{catalogo: aos441CatalogoRenomeado}).servidor(t)
		wal := filepath.Join(dir, "divergente.wal")
		r := correrComEnv(t, envDoNo(srv.URL), bin, "serve", "--wal", wal, "--run", "run-441", "--goal", "ler-e-publicar",
			"--snapshot", snap, "--decompose-fixture", fixture)
		if r.code != exitErro {
			t.Fatalf("o serve com snapshot divergente tinha de sair com %d, saiu %d\n%s\n%s", exitErro, r.code, r.stdout, r.stderr)
		}
		if !strings.Contains(r.stderr, `"fs.read" não existe no nó`) || !strings.Contains(r.stderr, "doc_read") {
			t.Fatalf("a recusa tinha de nomear a divergência:\n%s", r.stderr)
		}
		if strings.Contains(r.stdout, "posse:") {
			t.Fatalf("o serve tomou posse do run com o snapshot divergente:\n%s", r.stdout)
		}
		if _, err := os.Stat(wal); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("o WAL não podia ter sido aberto antes de o snapshot ser conferido (stat: %v)", err)
		}
	})

	t.Run("conferido toma posse", func(t *testing.T) {
		srv := (&aos441No{catalogo: aos441CatalogoDoSnapshotComPerigo}).servidor(t)
		r := correrComEnv(t, envDoNo(srv.URL), bin, "serve", "--wal", filepath.Join(dir, "conferido.wal"), "--run", "run-441-ok",
			"--goal", "ler-e-publicar", "--snapshot", snap, "--decompose-fixture", fixture)
		if !strings.Contains(r.stdout, "snapshot: 2 tool(s) conferida(s) com o catálogo do nó") {
			t.Fatalf("o serve tinha de declarar a conferência do snapshot:\n%s\n%s", r.stdout, r.stderr)
		}
		if !strings.Contains(r.stdout, "posse: run=run-441-ok") {
			t.Fatalf("com o snapshot conferido o serve tinha de tomar posse:\n%s\n%s", r.stdout, r.stderr)
		}
	})
}

// Um catálogo que não se lê (transporte, credencial) é recusa — fail-closed — mas com o seu nome:
// «catálogo do nó ilegível», e não «o snapshot diverge». O snapshot não é devolvido.
func TestAOS441CatalogoIlegivelRecusa(t *testing.T) {
	cli := leitorFixo{err: errors.New("ligação recusada")}
	snap, err := conferirSnapshotComONo(context.Background(), cli, escreverTmp(t, aos408SnapshotComPerigo))
	if !errors.Is(err, ErrCatalogoDoNoIlegivel) || !strings.Contains(err.Error(), "ligação recusada") {
		t.Fatalf("um catálogo ilegível tinha de recusar, com a causa: %v", err)
	}
	if errors.Is(err, ErrSnapshotDivergeDoNo) || !strings.Contains(err.Error(), "catalogo de tools do no ilegivel") {
		t.Fatalf("uma falha de leitura não pode dizer que o snapshot diverge: %v", err)
	}
	if len(snap.Tools) != 0 {
		t.Fatalf("com a conferência falhada não se devolve snapshot, veio %+v", snap)
	}
	if _, err := conferirSnapshotComONo(context.Background(), leitorFixo{cat: aos441Catalogo()}, escreverTmp(t, aos408SnapshotComPerigo)); err != nil {
		t.Fatalf("CONTROLO: com o catálogo certo a conferência tinha de passar: %v", err)
	}
}

// leitorFixo é um [leitorDoCatalogo] sem servidor.
type leitorFixo struct {
	cat []toolDoNo
	err error
}

func (l leitorFixo) CatalogoDeTools(context.Context) ([]toolDoNo, error) { return l.cat, l.err }
