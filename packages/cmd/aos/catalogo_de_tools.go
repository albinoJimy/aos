package main

// catalogo_de_tools.go — O CATÁLOGO DE TOOLS DO NÓ, legível por quem planeia (AOS-441).
//
// # O defeito que isto fecha
//
// O `aos-orq` decide o risco de um plano a partir de um snapshot PINADO de capabilities, e o nó
// executa cada nó do plano com as tools desse nó como lista-branca, comparadas PELO NOME com as
// tools que ele próprio oferece ao modelo (`AOS_MODEL_TOOLS`). Os dois lados nunca se
// comparavam: o snapshot de produção nomeou `fs.read` durante semanas enquanto o nó lhe chamava
// `doc_read`, e ninguém reparou — um plano que pedisse a tool ficava sem nenhuma utilizável.
//
// Esta rota é a metade do NÓ dessa comparação: diz, a quem já fala com ele, que tools oferece e
// com que contrato. A outra metade — recusar um snapshot que diverge — vive no `aos-orq`, no
// arranque do `consume` e do `serve` (snapshot.go de lá).
//
// # O que se serve, e porque é isto e não mais
//
// Por tool: o NOME (o `ToolID` que a lista-branca compara), a VERSÃO e o DIGEST do contrato, e os
// DOIS eixos de risco que o manifesto do nó declara — `egress` e `reversibility`.
//
// O DIGEST É UM PIN DO CONTRATO, NÃO PROVA DE REGISTO ASSINADO. Calcula-se pela MESMA fórmula que
// o registo assinado usaria (`AOS_MODEL_TOOLS_REGISTER`, modelcatalog.go — um teste amarra os
// dois), mas em produção esse registo vem desligado e nada é assinado: o valor diz «este é o
// contrato que o nó oferece», e nada mais. Cobre o contrato `KindTool` — schema de entrada,
// scopes de credencial e egress. NÃO cobre a capability, o recurso (`resource_*`), a
// reversibilidade, a descrição nem o binding de sandbox: mudar qualquer destes não muda o digest,
// e é por isso que a reversibilidade viaja e se compara à parte.
//
// Os eixos saem FAIL-CLOSED, na convenção do `aos-orq`: um `egress` que o manifesto não declara é
// `unknown` (o registo trata-o como `none` para o digest, mas o catálogo não afirma o que ninguém
// declarou); uma `reversibility` que não seja «reversible» é `irreversible` (a semântica do nó —
// ver [modelToolSpec]).
//
// NÃO se serve a SENSIBILIDADE, porque o manifesto do nó não a declara, e inventá-la aqui seria
// fabricar um eixo de risco que o classificador consumiria como facto.
//
// # Porque é plano de DADOS, com a mesma autenticação do `GET /runs/{id}`
//
// É uma LEITURA sem payload do chamador. Com o gate soberano composto (produção: o
// `AOS_BOARD_REGIONS` é obrigatório) exige a credencial forte — a MESMA que o `aos-orq` já
// apresenta ao `POST /plans/claim` e ao `POST /plans/outcome`. Sem o gate (dev/CI) serve pelo
// read-path legado, como o `GET /runs/{id}`: o catálogo não tem dados de titular, e recusar aqui
// fora de produção partiria o `serve` do `aos-orq` contra um nó de desenvolvimento sem proteger
// nada que o `GET /runs/{id}` já não exponha.

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
)

// versaoDoCatalogoDoNo é a versão com que o registo assinado pina CADA tool de AOS_MODEL_TOOLS
// (modelcatalog.go constrói `domain.Version{Major: 1}` para todas). O manifesto do nó não versiona
// tools; servir outra coisa seria afirmar uma versão que ninguém declarou.
var versaoDoCatalogoDoNo = domain.Version{Major: 1, Minor: 0, Patch: 0}

// egressNaoDeclarado é o que o catálogo serve para uma tool cujo manifesto não declara `egress`:
// o nome fail-closed do eixo no `aos-orq` (snapshot.go de lá), que o conta como externo.
const egressNaoDeclarado = "unknown"

// entradaDoCatalogo é UMA tool que o nó oferece ao modelo, na forma do fio de `GET /tools`.
type entradaDoCatalogo struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	Digest        string `json:"digest"`
	Egress        string `json:"egress"`
	Reversibility string `json:"reversibility"`
}

// respostaDoCatalogo é o corpo de `GET /tools`. `tools` está SEMPRE presente — vazio quando o nó
// não oferece tools ao modelo — para que o consumidor distinga «o nó não tem tools» de uma
// resposta mal formada.
type respostaDoCatalogo struct {
	Tools []entradaDoCatalogo `json:"tools"`
}

// contratoDaTool constrói o contrato de supply-chain de uma tool EXACTAMENTE como o registo
// assinado o constrói (parseSignedToolRegistryFromEnv). Um teste amarra os dois digests: se um
// dos lados mudar a forma do contrato, o catálogo servido deixa de bater com o registo e o teste
// avermelha.
func contratoDaTool(s modelToolSpec) (domain.Contract, error) {
	egress, err := parseEgressClass(s.Egress)
	if err != nil {
		return domain.Contract{}, err
	}
	return domain.Contract{
		InputSchema:      s.Parameters,
		CredentialScopes: append([]string(nil), s.CredentialScopes...),
		Egress:           egress,
	}, nil
}

// catalogoDeToolsDoAmbiente compõe o catálogo a partir do MESMO manifesto que o nó oferece ao
// modelo. Sem `AOS_MODEL_ENDPOINT` o nó não oferece tools nenhumas (parseModelFromEnv sai antes de
// as ler), e o catálogo é vazio — que é a verdade, e que o `aos-orq` recusa por si.
//
// Fail-closed: um manifesto que o nó não consegue ler já abortou o arranque noutro sítio; aqui
// aborta também um `egress` ou uma `reversibility` que não se reconhecem, em vez de servir um
// eixo de risco adivinhado.
func catalogoDeToolsDoAmbiente() ([]entradaDoCatalogo, error) {
	if strings.TrimSpace(os.Getenv("AOS_MODEL_ENDPOINT")) == "" {
		return []entradaDoCatalogo{}, nil
	}
	specs, err := readModelToolSpecs()
	if err != nil {
		return nil, err
	}
	cat := make([]entradaDoCatalogo, 0, len(specs))
	for _, s := range specs {
		name := strings.TrimSpace(s.Name)
		contrato, err := contratoDaTool(s)
		if err != nil {
			// Não se embrulha o erro do parser: ele fala do AOS_MODEL_TOOLS_REGISTER, que pode
			// estar desligado — a mensagem mandaria o operador olhar para o sítio errado.
			return nil, fmt.Errorf("%w: catalogo de tools (AOS-441): tool %q com egress %q — tem de ser none|internal|external",
				ErrBadModelTools, name, s.Egress)
		}
		rev, err := validateReversibility(s.Reversibility)
		if err != nil {
			return nil, fmt.Errorf("catalogo de tools (AOS-441): tool %q: %w", name, err)
		}
		// A semântica do nó: só «reversible» conta; o vazio vale IRREVERSÍVEL.
		if rev != "reversible" {
			rev = "irreversible"
		}
		// Egress NÃO declarado ⇒ `unknown`, e não o `none` que o contrato usa para o digest: o
		// catálogo é lido como facto de risco, e um eixo que ninguém declarou é o pior caso.
		egress := string(contrato.Egress)
		if strings.TrimSpace(s.Egress) == "" {
			egress = egressNaoDeclarado
		}
		cat = append(cat, entradaDoCatalogo{
			Name:          name,
			Version:       versaoDoCatalogoDoNo.String(),
			Digest:        digest.SHA256Digester{}.Digest(domain.KindTool, contrato),
			Egress:        egress,
			Reversibility: rev,
		})
	}
	sort.Slice(cat, func(i, j int) bool { return cat[i].Name < cat[j].Name })
	return cat, nil
}

// WithToolCatalog compõe o catálogo que `GET /tools` serve. Sem esta opção a rota serve um
// catálogo VAZIO — o `aos-orq` recusa então qualquer snapshot, que é o lado seguro.
func WithToolCatalog(cat []entradaDoCatalogo) APIOption {
	return func(c *apiConfig) {
		c.toolCatalog = append([]entradaDoCatalogo(nil), cat...)
	}
}

// handleToolCatalog serve `GET /tools`.
func (h *apiHandler) handleToolCatalog(w http.ResponseWriter, r *http.Request) {
	// Com o gate soberano composto, a mesma credencial e a mesma recusa das rotas irmãs do
	// `aos-orq` (`/plans/claim`, `/plans/outcome`). Sem ele, o read-path legado — ver o cabeçalho.
	if h.readGov != nil {
		quem, ok := h.readGov.authorize(r)
		if !ok || quem.principal == "" {
			writeError(w, http.StatusForbidden, "nao autorizado")
			return
		}
	}
	tools := h.cfg.toolCatalog
	if tools == nil {
		tools = []entradaDoCatalogo{}
	}
	writeJSON(w, http.StatusOK, respostaDoCatalogo{Tools: tools})
}
