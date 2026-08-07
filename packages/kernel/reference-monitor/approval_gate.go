package referencemonitor

import (
	"context"
	"crypto/sha256"
)

// ApprovalProof é a PROVA VERIFICADA de que humanos com autoridade assumiram uma tool
// call concreta (ADR-013/ADR-016). É produzida por um [ApprovalVerifier] APÓS verificação
// criptográfica e só é escrita no [Call] pelo [ApprovalGate] — nenhum pacote externo a
// consegue colocar lá (o campo é não-exportado).
//
// Não contém segredos: só a atribuição (quem assumiu) e a forma do controlo, para o audit
// registar QUEM destravou a acção.
type ApprovalProof struct {
	// Approvers são os principals que aprovaram (>= 2 e estruturalmente distintos quando
	// DualControl).
	Approvers []string
	// DualControl indica que a acção exigiu — e obteve — duas aprovações distintas.
	DualControl bool
}

// ApprovalVerifier verifica a EVIDÊNCIA de aprovação humana e confirma que ela está
// ligada à call concreta (pela preview). É a porta do gate; o adaptador real vive no
// composition root, sobre o four-eyes (dual-control, distinção por principal/sessão/
// credencial, challenges anti-replay, attestation de dispositivo, TTL da aprovação).
//
// CONTRATO: devolver (proof, nil) SÓ quando TUDO verifica — assinaturas válidas, pernas
// suficientes e distintas, challenge não reutilizado, aprovação dentro do TTL, e a
// preview assinada IGUAL à preview desta call. Qualquer outra situação devolve erro. O
// gate é fail-closed: qualquer erro ⇒ a call segue SEM aprovação (não é uma negação —
// é a ausência da excepção; a cadeia normal decide).
type ApprovalVerifier interface {
	VerifyApproval(ctx context.Context, evidence, preview []byte) (ApprovalProof, error)
}

// ApprovalPreview é o digest CANÓNICO da tool call — o valor que é EXIBIDO ao humano e
// que cada perna de aprovação assina (WYSIWYS). É a amarra que impede uma aprovação de
// ser reencaminhada para outra acção: muda o run, o passo, a tool, a capability, o
// recurso, o principal ou UM BYTE do input, e o digest muda ⇒ a aprovação não valida.
//
// Determinista e sem relógio: o nó calcula-o para MOSTRAR a acção pendente, e o
// [ApprovalGate] recalcula-o para VERIFICAR — se divergirem, a aprovação é de outra coisa.
//
// O Input entra por HASH (nunca em claro): o digest não deve transportar o payload, que
// pode conter dados sensíveis, mas tem de o cobrir.
func ApprovalPreview(c Call) []byte {
	h := sha256.New()
	write := func(s string) {
		_, _ = h.Write([]byte(s))
		_, _ = h.Write([]byte{0})
	}
	write(c.RunID)
	write(c.StepID)
	write(c.ToolID)
	write(c.Capability)
	write(c.Resource.Type)
	write(c.Resource.Value)
	write(c.Resource.Region)
	write(c.Principal.NHIID)
	inputSum := sha256.Sum256(c.Input)
	_, _ = h.Write(inputSum[:])
	return h.Sum(nil)
}

// ApprovalGate é o hook que transforma EVIDÊNCIA de aprovação humana (bytes opacos e
// untrusted, fornecidos pelo chamador em [Call.ApprovalEvidence]) numa PROVA VERIFICADA
// ligada a esta call — a única coisa que o [TaintGate] aceita como excepção à barreira
// «untrusted não comanda» (P4/AOS-069).
//
// # Porque vive no kernel (e não no pilar)
//
// Para a prova ser INFALSIFICÁVEL ela tem de ser escrita num campo NÃO-EXPORTADO do
// [Call], e só código deste pacote o consegue fazer — o mesmo mecanismo estrutural que
// torna [Decision.permit] não-forjável. Um gate que vivesse noutro pacote teria de usar
// um campo exportado, e então qualquer componente poderia "aprovar-se" a si próprio. A
// POLÍTICA (quem pode aprovar, quantas pernas, que frescura) fica externalizada na porta
// [ApprovalVerifier], como o [PrivilegedAuthorizer] faz para o taint.
//
// # Fail-closed
//
// Sem verificador, sem evidência, ou com evidência que não verifica, o gate é um NO-OP
// silencioso: NÃO nega (não é ele o gate de autorização) e NÃO marca a call. A cadeia
// segue e o [TaintGate] nega como negaria sem aprovação nenhuma. Assim, uma falha do
// subsistema de aprovação nunca ABRE nada — no máximo mantém tudo fechado.
//
// A evidência NUNCA é gravada no audit (pode conter material de credencial); só a
// atribuição resultante (os aprovadores) entra no registo.
type ApprovalGate struct {
	verifier ApprovalVerifier
}

// NewApprovalGate constrói o gate sobre um verificador. Um verificador nil torna o gate
// inerte (nenhuma call é jamais marcada como aprovada) — a postura de um deployment sem
// four-eyes provisionado, em que um escalate degrada para negação.
func NewApprovalGate(v ApprovalVerifier) ApprovalGate { return ApprovalGate{verifier: v} }

// Name implementa [Hook].
func (ApprovalGate) Name() string { return "approval" }

// Evaluate implementa [Hook]. Verifica a evidência e, em sucesso, marca a call com a
// prova. NUNCA nega — a decisão de autorização é dos gates a jusante.
func (g ApprovalGate) Evaluate(ctx context.Context, call *Call) (HookResult, error) {
	// Higiene: qualquer prova que já viesse no Call é DESCARTADA antes de qualquer
	// verificação. Um chamador não pode "trazer" uma aprovação — só evidência a
	// verificar. Espelha o IdentityCheck, que substitui o Principal após Verify.
	call.humanApproved = nil

	if g.verifier == nil || len(call.ApprovalEvidence) == 0 {
		return allow, nil // nada a verificar ⇒ a call segue sem excepção
	}
	proof, err := g.verifier.VerifyApproval(ctx, call.ApprovalEvidence, ApprovalPreview(*call))
	if err != nil {
		// Evidência inválida/expirada/de outra acção: a call segue SEM aprovação. Não se
		// nega aqui — se a capability for privilegiada, o TaintGate nega a jusante.
		return allow, nil
	}
	call.humanApproved = &proof
	return allow, nil
}

// HumanApproval devolve a prova de aprovação humana VERIFICADA desta call (nil se não
// houver). É só-leitura para fora do pacote: o valor só pode ter sido colocado pelo
// [ApprovalGate] após verificação. Serve o audit/observabilidade (atribuição de quem
// destravou a acção) sem abrir caminho a forjá-la.
func (c *Call) HumanApproval() *ApprovalProof { return c.humanApproved }
