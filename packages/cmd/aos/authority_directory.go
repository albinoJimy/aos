package main

// Directório de AUTORIDADE externo (AOS-071) — a via de provisionamento que faltava.
//
// O ScopeGate compõe DUAS fontes complementares: a autoridade derivada do TOKEN NHI
// verificado (AOS-156, dinâmica e por-mint) e um directório EXTERNO. Sem directório, o
// gate acaba a verificar `capability ∈ token.Scope` — que o hook de identidade já impôs.
// Não é vulnerabilidade (o grant é assinado pelo issuer e o eixo utilizador ∩ classe é
// computado no mint), mas a SEGUNDA OPINIÃO INDEPENDENTE do AOS-071 fica dormente, e com
// ela a REVOGAÇÃO: um token válido continua válido até expirar, aconteça o que acontecer
// à organização.
//
// [Config.Authority] existia mas era inalcançável pelo binário entregue — só atribuível
// por código. É o mesmo defeito de campo-fantasma que AOS_RATIFIERS/AOS_APPROVERS_FILE
// fecharam para os seus subsistemas.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/aos-ref/kernel/reference-monitor/authz"
)

// ErrBadAuthorityFile — AOS_AUTHORITY_FILE presente mas ilegível, com esquema inválido,
// sem sujeitos, ou com um sujeito DUPLICADO. Fail-closed de config: um directório que não
// se consegue ler não pode degradar em silêncio para "sem restrição", porque é
// exactamente nessa degradação que uma revogação deixaria de ser aplicada.
//
// O DUPLICADO aborta em vez de "o último ganha": duas entradas para o mesmo sujeito são
// um conflito de autoridade decidido por acidente de ordenação — e num sentido perigoso,
// porque a entrada mais permissiva pode apagar uma revogação.
var ErrBadAuthorityFile = errors.New("aos: AOS_AUTHORITY_FILE invalido (esperado JSON {\"subjects\":[{\"subject\":\"human:alice|agt-1\",\"capabilities\":[\"cap:fs.read\"]}]} — ficheiro ilegivel, esquema invalido, sujeito vazio, sujeito DUPLICADO ou lista vazia abortam em vez de deixarem o directorio silenciosamente sem efeito; para REVOGAR um sujeito, liste-o com \"capabilities\": [])")

// authorityDoc é o esquema do ficheiro de autoridade. Só material de POLÍTICA: sujeitos e
// as capabilities que a organização lhes reconhece. Nenhum segredo.
type authorityDoc struct {
	// Revision é o número de revisão do directório (observabilidade/rotação). Opcional.
	Revision uint64 `json:"revision,omitempty"`
	// Subjects são as entradas. Um sujeito é um humano ("human:alice") ou um agente
	// ("agt-1") — os mesmos identificadores da cadeia de delegação (ADR-003).
	Subjects []authoritySubjectDoc `json:"subjects"`
}

// authoritySubjectDoc é UMA entrada do directório.
type authoritySubjectDoc struct {
	Subject string `json:"subject"`
	// Capabilities é o que a organização reconhece a este sujeito. Uma lista VAZIA é
	// significativa e é a forma canónica de REVOGAR (ver [parseAuthorityFile]).
	Capabilities []string `json:"capabilities"`
}

// authorityDirectory é o resultado do parse. É ELE a [authz.AuthoritySource] injectada —
// e não a fonte estática nua — para carregar consigo os metadados que o banner declara
// (quantos sujeitos, quantos revogados, que revisão, que fingerprint). Mesmo molde da
// fonte de autoridade board→região, que também expõe Len/Revision/Fingerprint: uma
// rotação tem de ser VISÍVEL nos logs sem os encher com a lista de sujeitos.
type authorityDirectory struct {
	Source      authz.AuthoritySource
	Subjects    int
	Revoked     int
	Revision    uint64
	Fingerprint string
}

// Authority implementa [authz.AuthoritySource] delegando na fonte estática construída no
// parse. Não reimplementa a resolução: o directório é só um invólucro com metadados.
func (d *authorityDirectory) Authority(subject string) ([]string, bool) {
	return d.Source.Authority(subject)
}

// parseAuthorityFile lê o directório de autoridade MONTADO apontado por AOS_AUTHORITY_FILE.
// Vazio ⇒ (nil, nil): não configurado, e o ScopeGate opera só sobre a autoridade do token
// (comportamento actual, byte-idêntico).
//
// # A semântica que é preciso ter presente para operar isto
//
// O directório só RESTRINGE. Para um sujeito que ambas as fontes conhecem, o escopo
// efectivo é `token ∩ directório`; o directório nunca amplia o que o issuer assinou.
//
// E — a parte que engana — um sujeito AUSENTE do directório NÃO é restringido: cai na
// autoridade do token. É o que torna seguro ligar um directório PARCIAL (nada existente
// deixa de funcionar), mas significa que **revogar não é remover**. Remover uma entrada
// devolve o sujeito à autoridade plena do seu token. Para revogar, liste-o com
// `"capabilities": []`: a intersecção com o conjunto vazio é vazia e tudo lhe é negado.
//
// # Porque NÃO é assinado (ao contrário do bundle de política)
//
// Porque só pode restringir. Um adversário que adultere este ficheiro consegue NEGAR
// acções — uma negação de serviço, visível e auditável — mas não consegue conceder
// nenhuma: a ampliação está estruturalmente fora do alcance do gate, que intersecta com o
// grant ASSINADO do token. A integridade do ficheiro é a do volume montado, como no
// roster de aprovadores.
func parseAuthorityFile(path string) (*authorityDirectory, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil // não configurado ⇒ AOS-071 opera só sobre o token (declaradamente)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: ler %q: %v", ErrBadAuthorityFile, path, err)
	}
	var doc authorityDoc
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrBadAuthorityFile, path, err)
	}
	if len(doc.Subjects) == 0 {
		return nil, fmt.Errorf("%w: %q sem sujeitos (um ficheiro configurado e vazio deixaria o directorio sem QUALQUER efeito, exactamente como nao o ter — e quem o configurou julgaria ter revogacao)", ErrBadAuthorityFile, path)
	}

	src := authz.NewStaticAuthoritySource()
	seen := make(map[string]struct{}, len(doc.Subjects))
	revoked := 0
	for i, s := range doc.Subjects {
		subject := strings.TrimSpace(s.Subject)
		if subject == "" {
			return nil, fmt.Errorf("%w: %q entrada #%d com sujeito vazio", ErrBadAuthorityFile, path, i)
		}
		if _, dup := seen[subject]; dup {
			return nil, fmt.Errorf("%w: %q sujeito %q DUPLICADO (conflito de autoridade decidido por ordenacao — a entrada mais permissiva podia apagar uma revogacao)", ErrBadAuthorityFile, path, subject)
		}
		seen[subject] = struct{}{}
		caps := splitTrimmed(s.Capabilities)
		if len(caps) == 0 {
			revoked++ // entrada presente com conjunto vazio: REVOGAÇÃO explícita
		}
		src.Set(subject, caps...)
	}
	return &authorityDirectory{
		Source:      src,
		Subjects:    len(doc.Subjects),
		Revoked:     revoked,
		Revision:    doc.Revision,
		Fingerprint: authorityFingerprint(doc),
	}, nil
}

// authorityFingerprint resume o CONTEÚDO do directório num identificador curto e estável,
// para o banner declarar QUAL directório está em vigor e uma rotação ser visível nos logs
// sem os encher com a lista de sujeitos. Determinístico: sujeitos e capabilities ordenados.
func authorityFingerprint(doc authorityDoc) string {
	subs := make([]authoritySubjectDoc, len(doc.Subjects))
	copy(subs, doc.Subjects)
	sort.Slice(subs, func(i, j int) bool { return subs[i].Subject < subs[j].Subject })
	h := sha256.New()
	for _, s := range subs {
		_, _ = h.Write([]byte(strings.TrimSpace(s.Subject)))
		_, _ = h.Write([]byte{0})
		caps := splitTrimmed(s.Capabilities)
		sort.Strings(caps)
		for _, c := range caps {
			_, _ = h.Write([]byte(c))
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
