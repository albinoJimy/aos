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
	return nil
}

// SigningInput é a forma CANÓNICA que o humano assina: o domínio e cada campo como netstring
// (`<len>:<valor>,`), por ordem fixa, com o escopo ORDENADO. A codificação é injectiva — nenhum
// par de mandatos distintos produz os mesmos bytes — e não depende da ordem das chaves de um JSON.
func (m Mandate) SigningInput() []byte {
	var b strings.Builder
	ns := func(s string) {
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
		b.WriteByte(',')
	}
	ns(mandateDomain)
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
// dele e consulta a revogação do mandato. Devolve o ID do mandato verificado.
func (v *Verifier) verifyMandate(ctx context.Context, c Claims) (string, error) {
	if c.Mandate == nil {
		return "", fmt.Errorf("%w: iss=%q so e aceite com mandato embebido", ErrMandateRequired, c.Issuer)
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
		return "", fmt.Errorf("%w: nbf (%d) diferente de iat (%d) num emissor mandatado — o TTL do mandato conta a partir de agora", ErrMandateViolated, c.NotBefore, c.IssuedAt)
	}
	// UM SÓ ELO. O emissor honesto cunha sempre a raiz (humano → agente); os elos intermédios não
	// dão autoridade (o escopo é intersectado), mas escrevem na auditoria agentes que o humano não
	// autorizou, e a profundidade da cadeia passa a ser escolhida pelo emissor.
	if len(c.DelegationChain) != 1 {
		return "", fmt.Errorf("%w: cadeia com %d elos num emissor mandatado (so a raiz humano->agente)", ErrMandateViolated, len(c.DelegationChain))
	}
	sm := *c.Mandate
	pub, ok := v.mandateSigners[sm.Mandate.Human]
	if !ok {
		return "", fmt.Errorf("%w: nenhuma chave pinada para o humano %q", ErrMandateInvalid, sm.Mandate.Human)
	}
	if err := sm.VerifySignature(pub); err != nil {
		return "", err
	}
	if err := sm.Mandate.Covers(c); err != nil {
		return "", err
	}
	if v.revocations != nil {
		revoked, rerr := v.revocations.IsRevoked(ctx, MandateRevocationKey(sm.Mandate.ID))
		if rerr != nil {
			return "", fmt.Errorf("%w: %w", ErrRevocationUnavailable, rerr)
		}
		if revoked {
			return "", fmt.Errorf("%w: id=%q", ErrMandateRevoked, sm.Mandate.ID)
		}
	}
	return sm.Mandate.ID, nil
}
