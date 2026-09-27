package identity

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// # O MANDATO (AOS-427, ADR-033)
//
// Um mandato é um documento que um HUMANO assina UMA vez, com a sua própria chave, e que
// autoriza um emissor AUTOMÁTICO a cunhar NHIs em seu nome — dentro de limites que o próprio
// documento fixa. É a «delegação de longa duração» do ADR-032 §2.1, com o nome mudado para não
// colidir com a cadeia `delegation` (que é outra coisa: os elos on-behalf-of DENTRO de um token).
//
// # PORQUE É QUE O NÓ O VERIFICA, E NÃO SÓ O EMISSOR
//
// O emissor automático vive no servidor, com a chave no Vault transit. O Vault desse servidor
// destrava-se sozinho, logo quem comprometer o EMISSOR — o seu contentor, o token do Vault que ele
// usa, a chave transit — pode pedir assinaturas. Se o nó só verificasse a assinatura do emissor —
// que é o que faz para o emissor manual — esse atacante cunharia QUALQUER identidade.
//
// O QUE ISTO NÃO COBRE, declarado: root no host onde corre o NÓ. Esse muda a configuração do nó
// (AOS_MANDATE_SIGNERS, AOS_ISSUER_PUBKEY) e reinicia-o — nenhuma verificação dentro de um processo
// protege contra quem reescreve o processo. O mandato limita o emissor, não o anfitrião.
//
// Com o mandato EMBEBIDO no token e verificado pelo NÓ contra a chave do humano pinada no nó, o
// raio de acção de um emissor comprometido passa a ser exactamente o mandato: o humano, o agente,
// a classe, a política, o board, o escopo, o TTL máximo e a janela de validade que o humano
// assinou. Fora disso o nó recusa, e a chave do humano nunca esteve no servidor.
//
// Um emissor honesto verifica o mesmo antes de assinar ([Issuer.Issue] com
// [IssueRequest.Mandate]), mas essa verificação é CORTESIA: quem decide é o nó.

// MandatoValidadeMaxima é o tecto da janela de validade de um mandato (NotAfter − NotBefore).
//
// Um mandato é o artefacto de mais alto valor da cunhagem automática: enquanto vive, autoriza
// cunhagens sem humano presente. Sem tecto, um humano podia assinar um mandato «para sempre» e a
// única defesa seria a revogação — que só funciona se alguém se lembrar de revogar. Com tecto, o
// pior caso esquecido acaba sozinho. Constante, e não configuração, pela mesma razão do
// [TTLMaximo]: um tecto configurável é um tecto que um deployment novo volta a poder levantar.
const MandatoValidadeMaxima = 90 * 24 * time.Hour

// mandateDomain separa a assinatura de um mandato de qualquer outra assinatura que a mesma chave
// humana possa produzir (aprovações HITL, ratificações). Sem domínio, uma assinatura capturada
// noutro protocolo podia, em teoria, ser reinterpretada como mandato.
const mandateDomain = "aos.identity.mandate.v1"

// mandateDomainV2 é o domínio de um mandato que enumera `requesters` (AOS-439). Domínio NOVO, e
// não o v1 com um campo a mais: os bytes que um humano assinou em v1 continuam a ser exactamente
// os de antes, e um mandato v2 a que se arranquem os `requesters` passa a pedir o domínio v1 —
// cuja assinatura o humano nunca produziu para aquele conteúdo. Não há forma de converter um no
// outro sem a chave do humano.
const mandateDomainV2 = "aos.identity.mandate.v2"

// MandatoRequerentesMaximo é o tecto de `requesters` de um mandato. Um mandato viaja embebido em
// CADA token; uma lista sem tecto faria de cada token um documento. 64 cobre uma equipa; mais do
// que isso é um grupo, e grupos não são o que um humano deve assinar nome a nome.
const MandatoRequerentesMaximo = 64

// mandateRevocationPrefix é o espaço de nomes da revogação de mandatos no MESMO registo durável
// dos jti ([Revocations]). Um jti é aleatório em base64url (sem ':'), pelo que o prefixo não pode
// colidir com a revogação de um token.
const mandateRevocationPrefix = "mandate:"

// MandateRevocationKey é a chave de revogação de um mandato: o valor a passar a
// `POST /nhi/revoke` (campo `jti`) para que NENHUM token cunhado sob ele volte a verificar.
func MandateRevocationKey(id string) string { return mandateRevocationPrefix + id }

// Mandate é o conteúdo que o humano assina. Todos os campos são obrigatórios: um campo vazio num
// mandato seria um curinga, e o mandato existe para NÃO haver curingas.
type Mandate struct {
	// ID identifica o mandato na revogação ([MandateRevocationKey]) e na auditoria.
	ID string `json:"id"`
	// Human é o user_id do humano (sem o prefixo `human:`). A chave que assina é a DELE, pinada
	// no nó por este mesmo nome.
	Human string `json:"human"`
	// Board é o board de soberania sob o qual as NHIs cunhadas actuam.
	Board string `json:"board"`
	// AgentID, AgentClass e PolicyRef fixam QUE agente pode ser cunhado. O PolicyRef entra porque
	// o PDP o lê: um emissor comprometido que o pudesse escolher escolheria a política.
	AgentID    string `json:"agent_id"`
	AgentClass string `json:"agent_class"`
	PolicyRef  string `json:"policy_ref"`
	// Scope é o TECTO do escopo: cada token cunhado tem escopo ⊆ Scope.
	Scope []string `json:"scope"`
	// Issuer é o único emissor autorizado a cunhar sob este mandato.
	Issuer string `json:"iss"`
	// MaxTTLSeconds é o TTL máximo de cada token cunhado (exp − iat), em (0, [TTLMaximo]].
	MaxTTLSeconds int64 `json:"max_ttl_s"`
	// NotBefore e NotAfter (segundos Unix) delimitam QUANDO se pode cunhar: todo o token tem de
	// nascer e morrer dentro da janela. NotAfter − NotBefore ≤ [MandatoValidadeMaxima].
	NotBefore int64 `json:"nbf"`
	NotAfter  int64 `json:"exp"`
	// Requesters são os SUBMISSORES por quem o emissor pode agir (AOS-439, emenda ao ADR-033
	// §2.1): o `sub` do ID-token de quem pede um plano, exactamente como o nó o grava no
	// `planrequest.submitted`. O nó recusa o `POST /runs` de um run cujo submissor (o
	// `requested_by`, que o NÓ deriva da reclamação — nunca do corpo) não esteja aqui. Um service
	// account só submete se estiver nomeado.
	//
	// OBRIGATÓRIO NA ASSINATURA ([SignMandate] recusa sem ele), sem curingas e sem repetidos. Um
	// mandato SEM `requesters` é um mandato v1, anterior a esta capacidade: verifica-se com o
	// domínio v1 e o nó só o aceita dentro da janela de migração ([WithMandateV1Until]).
	Requesters []string `json:"requesters,omitempty"`
}

// Version é a versão do formato do mandato: 2 quando enumera `requesters`, 1 quando não.
func (m Mandate) Version() int {
	if len(m.Requesters) > 0 {
		return 2
	}
	return 1
}

// AdmitsRequester diz se o submissor `requestedBy` está dentro dos `requesters` do mandato.
// Um mandato v1 (sem lista) não admite ninguém por esta via — a sua aceitação é decidida pela
// janela de migração, não por esta função.
func (m Mandate) AdmitsRequester(requestedBy string) bool {
	if requestedBy == "" {
		return false
	}
	for _, r := range m.Requesters {
		if r == requestedBy {
			return true
		}
	}
	return false
}

// SignedMandate é o mandato com a assinatura ed25519 do humano sobre [Mandate.SigningInput].
// É esta forma que viaja: no ficheiro que o humano entrega ao emissor e embebida em cada token
// cunhado sob ela ([Claims.Mandate]).
type SignedMandate struct {
	Mandate Mandate `json:"mandate"`
	// Signature é a assinatura em base64url (sem padding).
	Signature string `json:"sig"`
}

// NewMandateID devolve um identificador aleatório de 128 bits em base64url — o mesmo formato do
// jti, e pela mesma razão: tem de ser imprevisível e único.
func NewMandateID() (string, error) { return randomJTI() }

// Validate impõe a forma do mandato, independente de quem o assinou. Fail-closed: devolve
// [ErrMandateInvalid] a embrulhar a primeira violação.
func (m Mandate) Validate() error {
	obrig := []struct{ nome, v string }{
		{"id", m.ID}, {"human", m.Human}, {"board", m.Board}, {"agent_id", m.AgentID},
		{"agent_class", m.AgentClass}, {"policy_ref", m.PolicyRef}, {"iss", m.Issuer},
	}
	for _, c := range obrig {
		if strings.TrimSpace(c.v) == "" {
			return fmt.Errorf("%w: campo %q vazio", ErrMandateInvalid, c.nome)
		}
	}
	// O ID é o que se revoga, e a rota de revogação apara espaços: um ID com um espaço seria
	// revogado sob um nome e verificado sob outro. Só base64url, que é o que [NewMandateID] gera.
	if len(m.ID) > 64 || strings.Trim(m.ID, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
		return fmt.Errorf("%w: id %q fora do alfabeto base64url (ou mais de 64 caracteres)", ErrMandateInvalid, m.ID)
	}
	if strings.HasPrefix(m.Human, "human:") {
		return fmt.Errorf("%w: human leva o user_id sem o prefixo \"human:\"", ErrMandateInvalid)
	}
	if len(m.Scope) == 0 {
		return fmt.Errorf("%w: escopo vazio (um mandato sem escopo nao autoriza nada, e nao deve existir)", ErrMandateInvalid)
	}
	visto := make(map[string]bool, len(m.Scope))
	for _, s := range m.Scope {
		if strings.TrimSpace(s) == "" || visto[s] {
			return fmt.Errorf("%w: escopo com entrada vazia ou repetida", ErrMandateInvalid)
		}
		visto[s] = true
	}
	// OS TECTOS COMPARAM-SE EM SEGUNDOS INTEIROS. Converter para time.Duration multiplica por 1e9
	// e dá a volta com valores gigantes: um max_ttl_s de 18446744074 passava como «586 anos» abaixo
	// do tecto (achado da revisão adversarial). Do lado dos segundos não há multiplicação.
	if m.MaxTTLSeconds <= 0 || m.MaxTTLSeconds > int64(TTLMaximo/time.Second) {
		return fmt.Errorf("%w: max_ttl_s=%d fora de (0, %d]", ErrMandateInvalid, m.MaxTTLSeconds, int64(TTLMaximo/time.Second))
	}
	// Instantes POSITIVOS antes de subtrair: com nbf muito negativo, exp−nbf também dava a volta.
	if m.NotBefore <= 0 || m.NotAfter <= m.NotBefore {
		return fmt.Errorf("%w: janela vazia, invertida ou antes da epoch (nbf=%d, exp=%d)", ErrMandateInvalid, m.NotBefore, m.NotAfter)
	}
	if m.NotAfter-m.NotBefore > int64(MandatoValidadeMaxima/time.Second) {
		return fmt.Errorf("%w: janela de %ds acima do tecto de %s", ErrMandateInvalid, m.NotAfter-m.NotBefore, MandatoValidadeMaxima)
	}
	return validarRequerentes(m.Requesters)
}

// validarRequerentes impõe a forma dos `requesters` (AOS-439) quando existem. A AUSÊNCIA não é
// decidida aqui — é o que distingue v1 de v2, e quem a recusa é [SignMandate] (não se assina v1) e
// o verificador (fora da janela de migração).
//
// Cada entrada é um NOME EXACTO: sem espaços, sem curingas (`*`), sem vírgulas (a lista chega por
// CSV ao `mandate-sign`, e uma vírgula dentro de um nome seria dois nomes num lado e um no outro).
func validarRequerentes(rs []string) error {
	if len(rs) > MandatoRequerentesMaximo {
		return fmt.Errorf("%w: %d requesters, acima do tecto de %d", ErrMandateInvalid, len(rs), MandatoRequerentesMaximo)
	}
	visto := make(map[string]bool, len(rs))
	for _, r := range rs {
		if r == "" || len(r) > 256 || strings.ContainsAny(r, " \t\r\n,*") {
			return fmt.Errorf("%w: requester %q vazio, longo, com espaco, virgula ou curinga", ErrMandateInvalid, r)
		}
		if visto[r] {
			return fmt.Errorf("%w: requester %q repetido", ErrMandateInvalid, r)
		}
		visto[r] = true
	}
	return nil
}

// SigningInput é a forma CANÓNICA que o humano assina: o domínio e cada campo como netstring
// (`<len>:<valor>,`), por ordem fixa, com o escopo ORDENADO. A codificação é injectiva — nenhum
// par de mandatos distintos produz os mesmos bytes — e não depende da ordem das chaves de um JSON.
//
// v2 (AOS-439): com `requesters`, o domínio é [mandateDomainV2] e a lista ORDENADA vai no FIM,
// depois do escopo, com a contagem à frente. Sem eles, os bytes são exactamente os do v1 — um
// mandato já assinado continua a verificar.
func (m Mandate) SigningInput() []byte {
	var b strings.Builder
	ns := func(s string) {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
		b.WriteByte(',')
	}
	if m.Version() == 2 {
		ns(mandateDomainV2)
	} else {
		ns(mandateDomain)
	}
	ns(m.ID)
	ns(m.Human)
	ns(m.Board)
	ns(m.AgentID)
	ns(m.AgentClass)
	ns(m.PolicyRef)
	ns(m.Issuer)
	ns(strconv.FormatInt(m.MaxTTLSeconds, 10))
	ns(strconv.FormatInt(m.NotBefore, 10))
	ns(strconv.FormatInt(m.NotAfter, 10))
	scope := append([]string(nil), m.Scope...)
	sort.Strings(scope)
	ns(strconv.Itoa(len(scope)))
	for _, s := range scope {
		ns(s)
	}
	if m.Version() == 2 {
		rs := append([]string(nil), m.Requesters...)
		sort.Strings(rs)
		ns(strconv.Itoa(len(rs)))
		for _, r := range rs {
			ns(r)
		}
	}
	return []byte(b.String())
}

// SignMandate valida o mandato e assina-o com a chave do HUMANO, através de um [crypto.Signer]
// (a chave pode viver fora do processo). Nunca assina um mandato inválido.
func SignMandate(signer crypto.Signer, m Mandate) (SignedMandate, error) {
	if signer == nil {
		return SignedMandate{}, ErrInvalidSigner
	}
	if err := m.Validate(); err != nil {
		return SignedMandate{}, err
	}
	// JÁ NÃO SE ASSINA v1 (AOS-439). Um mandato sem `requesters` autoriza o emissor a agir por
	// QUALQUER submissor — é o curinga que o mandato existe para não ter. Os v1 já assinados
	// verificam dentro da janela de migração do nó; novos não nascem.
	if m.Version() < 2 {
		return SignedMandate{}, fmt.Errorf("%w: requesters vazio — um mandato tem de nomear por quem o emissor pode agir (v1 ja nao se assina)", ErrMandateInvalid)
	}
	pub, err := signerPublicKey(signer)
	if err != nil {
		return SignedMandate{}, err
	}
	sig, err := signer.Sign(rand.Reader, m.SigningInput(), crypto.Hash(0))
	if err != nil {
		return SignedMandate{}, fmt.Errorf("%w: %w", ErrInvalidSigner, err)
	}
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, m.SigningInput(), sig) {
		return SignedMandate{}, ErrInvalidSigner
	}
	return SignedMandate{Mandate: m, Signature: b64enc(sig)}, nil
}

// VerifySignature confirma que o mandato foi assinado pela chave `pub` e tem forma válida.
func (s SignedMandate) VerifySignature(pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: chave do signatario invalida", ErrMandateInvalid)
	}
	sig, err := base64.RawURLEncoding.DecodeString(s.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: assinatura malformada", ErrMandateInvalid)
	}
	if !ed25519.Verify(pub, s.Mandate.SigningInput(), sig) {
		return fmt.Errorf("%w: assinatura nao verifica com a chave pinada de %q", ErrMandateInvalid, s.Mandate.Human)
	}
	return s.Mandate.Validate()
}

// Covers verifica que um token está DENTRO do mandato: o emissor, a identidade (humano, agente,
// classe, política, board), o escopo, o TTL e a janela temporal. Não verifica a assinatura do
// mandato nem a revogação — isso é [SignedMandate.VerifySignature] e o verificador.
//
// Devolve [ErrMandateViolated] a nomear o PRIMEIRO campo fora do mandato: o operador que lê a
// recusa precisa de saber o que o emissor tentou, e nenhum destes campos é segredo.
func (m Mandate) Covers(c Claims) error {
	iguais := []struct{ nome, token, mandato string }{
		{"iss", c.Issuer, m.Issuer},
		{"user_id", c.UserID, m.Human},
		{"agent_id", c.AgentID, m.AgentID},
		{"agent_class", c.AgentClass, m.AgentClass},
		{"policy_ref", c.PolicyRef, m.PolicyRef},
		{"board", c.Board, m.Board},
	}
	for _, f := range iguais {
		if f.token != f.mandato {
			return fmt.Errorf("%w: %s do token (%q) difere do mandato (%q)", ErrMandateViolated, f.nome, f.token, f.mandato)
		}
	}
	if !authoritySubset(c.Scope, m.Scope) {
		return fmt.Errorf("%w: escopo do token excede o do mandato", ErrMandateViolated)
	}
	if c.Expiry-c.IssuedAt > m.MaxTTLSeconds {
		return fmt.Errorf("%w: TTL do token (%ds) acima do maximo do mandato (%ds)", ErrMandateViolated, c.Expiry-c.IssuedAt, m.MaxTTLSeconds)
	}
	if c.IssuedAt < m.NotBefore || c.Expiry > m.NotAfter {
		return fmt.Errorf("%w: token [%d, %d] fora da janela do mandato [%d, %d]", ErrMandateViolated,
			c.IssuedAt, c.Expiry, m.NotBefore, m.NotAfter)
	}
	return nil
}

// verifyMandate é o passo do [Verifier] para um emissor MANDATADO: exige o mandato embebido,
// verifica-o contra a chave PINADA do humano que ele nomeia, confirma que o token está dentro
// dele e consulta a revogação do mandato. Devolve o mandato verificado.
func (v *Verifier) verifyMandate(ctx context.Context, c Claims) (Mandate, error) {
	if c.Mandate == nil {
		return Mandate{}, fmt.Errorf("%w: iss=%q so e aceite com mandato embebido", ErrMandateRequired, c.Issuer)
	}
	// O TTL DO MANDATO CONTA A PARTIR DE AGORA, E NÃO DE UM iat QUE O EMISSOR ESCOLHE.
	//
	// O [Mandate.Covers] compara exp−iat com o TTL máximo — mas iat é uma afirmação do emissor, e o
	// emissor é exactamente quem aqui se assume comprometido. A revisão adversarial provou-o: um
	// token com iat = fim_do_mandato − 45m, exp = fim_do_mandato e nbf = 0 passava no Covers, e o
	// passo 4 do Verify salta o nbf quando é zero — um token de 45 minutos vivo durante 30 dias.
	// Amarra-se o iat ao relógio do NÓ exigindo nbf == iat (o [Issuer] honesto põe os dois
	// iguais): o passo 4 do Verify já recusou um nbf no futuro, logo iat ≤ agora + folga, e com o
	// Covers (exp − iat ≤ TTL máximo) fica exp ≤ agora + folga + TTL máximo. Um nbf a zero — que o
	// passo 4 salta — também cai aqui, porque o iat de um token nunca é zero.
	if c.NotBefore != c.IssuedAt {
		return Mandate{}, fmt.Errorf("%w: nbf (%d) diferente de iat (%d) num emissor mandatado — o TTL do mandato conta a partir de agora", ErrMandateViolated, c.NotBefore, c.IssuedAt)
	}
	// UM SÓ ELO. O emissor honesto cunha sempre a raiz (humano → agente); os elos intermédios não
	// dão autoridade (o escopo é intersectado), mas escrevem na auditoria agentes que o humano não
	// autorizou, e a profundidade da cadeia passa a ser escolhida pelo emissor.
	if len(c.DelegationChain) != 1 {
		return Mandate{}, fmt.Errorf("%w: cadeia com %d elos num emissor mandatado (so a raiz humano->agente)", ErrMandateViolated, len(c.DelegationChain))
	}
	sm := *c.Mandate
	pub, ok := v.mandateSigners[sm.Mandate.Human]
	if !ok {
		return Mandate{}, fmt.Errorf("%w: nenhuma chave pinada para o humano %q", ErrMandateInvalid, sm.Mandate.Human)
	}
	if err := sm.VerifySignature(pub); err != nil {
		return Mandate{}, err
	}
	// A JANELA DE MIGRAÇÃO DOS v1 (AOS-439). Depois da assinatura — só se decide sobre um mandato
	// que o humano pinado assinou mesmo — e antes de tudo o resto. Fora da janela, um v1 é recusado
	// mesmo sendo autêntico: autoriza o emissor a agir por QUALQUER submissor, e isso só se tolera
	// enquanto o humano não re-assina com `requesters`. A janela compara com o relógio do NÓ.
	if sm.Mandate.Version() < 2 && (v.mandateV1Until.IsZero() || !v.now().Before(v.mandateV1Until)) {
		return Mandate{}, fmt.Errorf("%w: id=%q (re-assinar com requesters: aos-issuer mandate-sign --requesters)", ErrMandateV1Closed, sm.Mandate.ID)
	}
	if err := sm.Mandate.Covers(c); err != nil {
		return Mandate{}, err
	}
	if v.revocations != nil {
		revoked, rerr := v.revocations.IsRevoked(ctx, MandateRevocationKey(sm.Mandate.ID))
		if rerr != nil {
			return Mandate{}, fmt.Errorf("%w: %w", ErrRevocationUnavailable, rerr)
		}
		if revoked {
			return Mandate{}, fmt.Errorf("%w: id=%q", ErrMandateRevoked, sm.Mandate.ID)
		}
	}
	return sm.Mandate, nil
}
