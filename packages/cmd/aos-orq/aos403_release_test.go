package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// AOS-403 — o `aos-orq` viaja na imagem assinada do nó e corre-se em produção pelo serviço
// `aos-orq` do compose. Estes testes prendem as três metades que um refactor separaria em
// silêncio: a allowlist de egress sob produção, o Dockerfile que o empacota e o serviço que o
// corre com a config que ele lê.

func TestAOS403_EgressDerivaDoEndpointSobProducao(t *testing.T) {
	t.Setenv("AOS_MODEL_NAME", "gpt-4o-mini")
	t.Setenv("AOS_MODEL_API_KEY_PATH", "/etc/aos/model-api.key")

	casos := []struct {
		nome, modo, endpoint, hosts string
		quer                        []string
	}{
		{"producao sem hosts deriva host:porta", "production", "https://litellm:4000/v1", "", []string{"litellm:4000"}},
		{"producao sem porta deriva so o host", "production", "https://api.example.com/v1", "", []string{"api.example.com"}},
		{"hosts explicitos ganham", "production", "https://litellm:4000/v1", "a.example:443, b.example", []string{"a.example:443", "b.example"}},
		{"IPv6 com porta mantem os brackets", "production", "https://[::1]:4000/v1", "", []string{"[::1]:4000"}},
		{"fora de producao nao deriva", "", "https://litellm:4000/v1", "", nil},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Setenv("AOS_MODE", c.modo)
			t.Setenv("AOS_MODEL_ENDPOINT", c.endpoint)
			t.Setenv("AOS_MODEL_EGRESS_HOSTS", c.hosts)
			cfg, err := gatewayConfigFromEnv()
			if err != nil || cfg == nil {
				t.Fatalf("cfg=%v err=%v", cfg, err)
			}
			if !reflect.DeepEqual(cfg.egressHosts, c.quer) {
				t.Errorf("egressHosts = %q, quer %q", cfg.egressHosts, c.quer)
			}
		})
	}

	t.Run("timeout de egress le a variavel do no", func(t *testing.T) {
		t.Setenv("AOS_MODE", "production")
		t.Setenv("AOS_MODEL_ENDPOINT", "https://litellm:4000/v1")
		t.Setenv("AOS_MODEL_EGRESS_HOSTS", "")
		t.Setenv("AOS_MODEL_EGRESS_TIMEOUT", "120s")
		cfg, err := gatewayConfigFromEnv()
		if err != nil || cfg == nil {
			t.Fatalf("cfg=%v err=%v", cfg, err)
		}
		if cfg.egressTimeout != 120*time.Second {
			t.Errorf("egressTimeout = %v, quer 120s", cfg.egressTimeout)
		}
		for _, mau := range []string{"1ms", "31m", "abc"} {
			t.Setenv("AOS_MODEL_EGRESS_TIMEOUT", mau)
			if _, err := gatewayConfigFromEnv(); err == nil {
				t.Errorf("AOS_MODEL_EGRESS_TIMEOUT=%q aceite — devia recusar", mau)
			}
		}
		t.Setenv("AOS_MODEL_EGRESS_TIMEOUT", "")
	})

	t.Run("producao com endpoint sem host recusa", func(t *testing.T) {
		t.Setenv("AOS_MODE", "production")
		t.Setenv("AOS_MODEL_ENDPOINT", "litellm-sem-esquema")
		t.Setenv("AOS_MODEL_EGRESS_HOSTS", "")
		if cfg, err := gatewayConfigFromEnv(); err == nil {
			t.Fatalf("endpoint sem host aceite sob produção: cfg=%+v", cfg)
		}
	})
}

func lerDoRepo(t *testing.T, partes ...string) string {
	t.Helper()
	caminho := filepath.Join(append([]string{"..", "..", ".."}, partes...)...)
	b, err := os.ReadFile(caminho)
	if err != nil {
		t.Fatalf("ler %s: %v", caminho, err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

func TestAOS403_ImagemEmpacotaOOrquestrador(t *testing.T) {
	df := lerDoRepo(t, "deploy", "node", "Dockerfile")
	for _, quer := range []string{
		"WORKDIR /src/packages/cmd/aos-orq\n",
		`RUN GOPROXY=off go build -trimpath -ldflags="-s -w -buildid=" -o /out/aos-orq .`,
		"COPY --from=builder /out/aos-orq /usr/local/bin/aos-orq\n",
		"WORKDIR /var/lib/aos-orq\n",
	} {
		if !strings.Contains(df, quer) {
			t.Errorf("Dockerfile sem %q", quer)
		}
	}
	// O ENTRYPOINT da imagem continua a ser o nó: o orquestrador corre-se por --entrypoint.
	if !strings.Contains(df, `ENTRYPOINT ["/usr/local/bin/aos"]`) {
		t.Error("o ENTRYPOINT da imagem deixou de ser o nó")
	}
	// O WORKDIR final tem de continuar a ser o do nó (o volume aos-data monta-se aí).
	idxOrq := strings.LastIndex(df, "WORKDIR /var/lib/aos-orq")
	idxNo := strings.LastIndex(df, "WORKDIR /var/lib/aos\n")
	if idxNo < idxOrq {
		t.Error("WORKDIR /var/lib/aos-orq vem depois do do nó — o WORKDIR final mudou")
	}
}

var reServicoCompose = regexp.MustCompile(`(?m)^  [a-z][a-z0-9-]*:\s*$`)

func blocoServico(t *testing.T, compose, nome string) string {
	t.Helper()
	cab := "\n  " + nome + ":\n"
	i := strings.Index(compose, cab)
	if i < 0 {
		t.Fatalf("serviço %q ausente do compose de produção", nome)
	}
	resto := compose[i+len(cab):]
	if j := reServicoCompose.FindStringIndex(resto); j != nil {
		resto = resto[:j[0]]
	}
	return resto
}

var reGetenvOrq = regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\("(AOS_[A-Z0-9_]+)"\)`)

func TestAOS403_ServicoDoComposeCorreOOrquestrador(t *testing.T) {
	compose := lerDoRepo(t, "deploy", "server", "docker-compose.prod.yml")
	bloco := blocoServico(t, compose, "aos-orq")

	for _, quer := range []string{
		"image: ${AOS_IMAGE:?",
		`profiles: ["orq"]`,
		`entrypoint: ["/usr/local/bin/aos-orq"]`,
		`restart: "no"`,
		"read_only: true",
		"- ALL",
		"disable: true",
		"- aos-orq-data:/var/lib/aos-orq",
		"- ./orq:/etc/aos-orq:ro",
		"- ./tls-internal/ca-bundle.crt:/etc/aos/internal-ca.crt:ro",
		"- ./secrets/model-api.key:/etc/aos/model-api.key:ro",
		`SSL_CERT_FILE: "${AOS_INTERNAL_CA_BUNDLE:-}"`,
		`AOS_MODEL_AUDIT_PATH: "${AOS_ORQ_MODEL_AUDIT_PATH:-/var/lib/aos-orq/model-audit.wal}"`,
	} {
		if !strings.Contains(bloco, quer) {
			t.Errorf("serviço aos-orq sem %q", quer)
		}
	}
	// O volume do nó NUNCA se monta no orquestrador: os dois pedem posse exclusiva do seu WORM.
	if strings.Contains(bloco, "aos-data:") {
		t.Error("o serviço aos-orq monta o volume do nó (aos-data)")
	}
	if !strings.Contains(compose, "\nvolumes:\n  aos-data:\n  aos-orq-data:\n") {
		t.Error("volume aos-orq-data não declarado no topo do compose")
	}

	// Toda a variável que o binário lê chega ao contentor (o bloco environment é um allowlist).
	entradas, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	lidas := map[string]bool{}
	for _, e := range entradas {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range reGetenvOrq.FindAllStringSubmatch(string(b), -1) {
			lidas[m[1]] = true
		}
	}
	if len(lidas) < 5 {
		t.Fatalf("só %d variáveis lidas pelo aos-orq — o varredor partiu-se", len(lidas))
	}
	var faltam []string
	for v := range lidas {
		if !strings.Contains(bloco, "      "+v+":") {
			faltam = append(faltam, v)
		}
	}
	sort.Strings(faltam)
	if len(faltam) > 0 {
		t.Errorf("o aos-orq lê estas variáveis mas o serviço aos-orq do compose não as passa: %v", faltam)
	}
}
