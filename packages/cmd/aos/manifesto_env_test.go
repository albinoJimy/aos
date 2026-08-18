package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestManifestoDeDeployPassaTodaAConfigQueONoLe fecha uma classe de defeito que já se manifestou
// TRÊS vezes neste projecto, sempre com o mesmo sintoma enganador.
//
// O bloco `environment` do docker-compose é um ALLOWLIST. Uma variável definida no `.env` mas
// ausente dele NÃO chega ao contentor — e o nó, que não a vê, anuncia no banner de arranque que a
// funcionalidade está desligada e diz "defina X para a ligar". Define-se X, nada muda, e o banner
// continua a pedir X. O operador conclui que o nó está partido; o nó está a dizer a verdade.
//
// Aconteceu com o driver gVisor, com AOS_CHALLENGE_ISSUANCE, e depois num levantamento que
// encontrou 38 variáveis INDEFINÍVEIS de uma só vez — oito conceitos do sistema estavam
// bloqueados por isto, e nenhum deles por decisão de ninguém.
//
// Este teste torna a omissão IMPOSSÍVEL DE PASSAR EM SILÊNCIO: uma variável nova lida pelo nó
// falha aqui até ser passada pelo manifesto, ou até ser EXCLUÍDA COM RAZÃO ESCRITA abaixo.
func TestManifestoDeDeployPassaTodaAConfigQueONoLe(t *testing.T) {
	lidas := envsLidasPeloNo(t)
	if len(lidas) < 50 {
		t.Fatalf("só encontrei %d variáveis lidas — o varredor partiu-se e o teste ficaria vacuoso", len(lidas))
	}
	passadas := envsDoManifesto(t)
	if len(passadas) < 50 {
		t.Fatalf("só encontrei %d variáveis no manifesto — o parser partiu-se e o teste ficaria vacuoso", len(passadas))
	}

	var faltam []string
	for _, v := range lidas {
		if passadas[v] || excluidasComRazao[v] != "" {
			continue
		}
		faltam = append(faltam, v)
	}
	sort.Strings(faltam)
	if len(faltam) > 0 {
		t.Errorf("o nó LÊ estas variáveis mas o manifesto de produção NÃO as passa — são\n"+
			"INDEFINÍVEIS, e o banner vai pedir que se definam algo que não chega ao contentor:\n  %s\n\n"+
			"Acrescente-as ao bloco `environment` de deploy/server/docker-compose.prod.yml como\n"+
			"`${VAR:-}` (vazio ⇒ ausente, sem mudar comportamento), ou declare-as em\n"+
			"excluidasComRazao com a razão pela qual NÃO devem ser definíveis.",
			strings.Join(faltam, "\n  "))
	}

	// CONTROLO da própria exclusão: uma entrada em excluidasComRazao que o nó já não lê é lixo
	// que dá cobertura falsa — passaria a esconder uma variável nova com o mesmo nome.
	conjunto := make(map[string]bool, len(lidas))
	for _, v := range lidas {
		conjunto[v] = true
	}
	for v := range excluidasComRazao {
		if !conjunto[v] {
			t.Errorf("excluidasComRazao tem %q, que o nó já NÃO lê — remova a entrada", v)
		}
	}
}

// excluidasComRazao são as variáveis que o nó lê e que o manifesto de produção NÃO deve passar.
// A razão fica aqui e não num comentário do YAML porque é uma decisão de postura, e este teste é
// quem a faz valer.
var excluidasComRazao = map[string]string{
	"AOS_ISSUER_KEY_PATH": "daria ao nó um caminho para uma CHAVE DE ASSINATURA. Este nó é " +
		"trust-anchor-only (AOS-156) — nenhuma chave de assinatura entra no runtime, e é isso " +
		"que faz com que código comprometido in-process não tenha material com que cunhar. " +
		"Tornar isto definível é oferecer a porta que a postura fecha.",
	"AOS_READER": "credencial de leitor DEMO-GRADE por variável de ambiente. Sob " +
		"AOS_MODE=production não autoriza nada (o board vem das claims), mas passá-la " +
		"convidaria a tentar e o sintoma seria uma recusa opaca.",
	"AOS_BOARD": "o par de AOS_READER — mesma razão.",
}

var reGetenv = regexp.MustCompile(`os\.Getenv\("(AOS_[A-Z0-9_]+)"\)`)

// envsLidasPeloNo varre os fontes deste pacote à procura de os.Getenv("AOS_..."), ignorando
// ficheiros de teste (uma env que só um teste lê não precisa de chegar a produção).
func envsLidasPeloNo(t *testing.T) []string {
	t.Helper()
	entradas, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ler o pacote: %v", err)
	}
	vistas := map[string]bool{}
	for _, e := range entradas {
		nome := e.Name()
		if e.IsDir() || !strings.HasSuffix(nome, ".go") || strings.HasSuffix(nome, "_test.go") {
			continue
		}
		b, err := os.ReadFile(nome)
		if err != nil {
			t.Fatalf("ler %s: %v", nome, err)
		}
		for _, m := range reGetenv.FindAllStringSubmatch(string(b), -1) {
			vistas[m[1]] = true
		}
	}
	out := make([]string, 0, len(vistas))
	for v := range vistas {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

var reManifesto = regexp.MustCompile(`(?m)^\s{6}(AOS_[A-Z0-9_]+):`)

// envsDoManifesto lê as chaves do bloco `environment` do compose de produção. Deliberadamente
// textual e não por parser de YAML: este pacote não tem dependência de YAML, e o que interessa
// verificar é exactamente o que lá está escrito.
func envsDoManifesto(t *testing.T) map[string]bool {
	t.Helper()
	caminho := filepath.Join("..", "..", "..", "deploy", "server", "docker-compose.prod.yml")
	b, err := os.ReadFile(caminho)
	if err != nil {
		// FALHA, não skip: sem o manifesto este teste não verifica nada, e um skip seria a
		// mesma cobertura falsa que ele existe para impedir.
		t.Fatalf("ler o manifesto de produção %s: %v", caminho, err)
	}
	out := map[string]bool{}
	for _, m := range reManifesto.FindAllStringSubmatch(string(b), -1) {
		out[m[1]] = true
	}
	return out
}
