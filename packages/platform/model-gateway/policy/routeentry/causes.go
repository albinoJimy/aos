package routeentry

import (
	"errors"
	"fmt"
)

// As causas de uma recusa. Vocabulário FECHADO: é o que aparece no erro de arranque do nó.
const (
	// Da pasta e da âncora.

	// CauseNoTrustAnchor — a pasta está configurada e a âncora de confiança não.
	CauseNoTrustAnchor = "pasta_sem_ancora"
	// CauseBadTrustAnchor — a âncora não é o base64 de uma chave pública ed25519.
	CauseBadTrustAnchor = "ancora_invalida"
	// CauseSharedTrustAnchor — a âncora dos perfis é a de outra política (a da allowlist). As
	// duas não são intermutáveis: quem pode autorizar um nome não fica, por isso, a poder dizer o
	// que o nome significa.
	CauseSharedTrustAnchor = "ancora_partilhada"
	// CauseDirUnreadable — a pasta não existe, não é uma pasta ou não se lê.
	CauseDirUnreadable = "pasta_ilegivel"
	// CauseUnknownFile — a pasta tem uma entrada que não é um dos três ficheiros de um registo:
	// outro nome, uma subpasta, uma ligação simbólica, ou uma assinatura ou um relatório sem registo.
	CauseUnknownFile = "ficheiro_nao_reconhecido"
	// CauseTooManyEntries — a pasta tem mais registos do que [MaxEntries].
	CauseTooManyEntries = "demasiados_registos"

	// Da assinatura.

	// CauseSignatureMissing — o registo não tem o ficheiro da assinatura ao lado.
	CauseSignatureMissing = "assinatura_em_falta"
	// CauseSignatureInvalid — a assinatura não se lê, ou não verifica contra a âncora (o registo
	// foi alterado depois de assinado, ou foi assinado com outra chave).
	CauseSignatureInvalid = "assinatura_invalida"

	// Do registo.

	// CauseEntryUnreadable — o registo não é o JSON de um registo: nome de membro desconhecido ou
	// repetido, valor de tipo errado, membro em falta, conteúdo depois do documento, ou maior do
	// que o tecto.
	CauseEntryUnreadable = "registo_ilegivel"
	// CauseUnknownFormat — a versão de formato do registo não é [EntryFormat].
	CauseUnknownFormat = "formato_desconhecido"
	// CauseShareOutOfRange — a fatia inicial não é um inteiro de 0 a 100.
	CauseShareOutOfRange = "fatia_fora_do_intervalo"
	// CauseBadProfile — o perfil que o registo traz não passa na leitura fechada do gateway.
	CauseBadProfile = "perfil_invalido"
	// CauseBadRouteName — o nome pedido do perfil não serve: tem um carácter que não cabe num nome
	// de stream (um ponto, por exemplo) ou passa do tecto.
	CauseBadRouteName = "nome_da_rota_invalido"
	// CauseProfileDigestMismatch — o digest do perfil que o registo declara não é o do perfil que traz.
	CauseProfileDigestMismatch = "digest_do_perfil_diferente"
	// CauseProfileReturnsState — o perfil devolve estado opaco ao provider. Por esta via só entram
	// perfis que não devolvem estado (decisão D8 do dono, 2026-10-11).
	CauseProfileReturnsState = "perfil_devolve_estado"
	// CauseOtherEnvironment — o registo foi assinado para outro board ou outra região.
	CauseOtherEnvironment = "registo_de_outro_ambiente"
	// CauseProxyImageNotDeclared — a imagem do proxy que o registo cita não é a declarada para
	// produção no repositório ([ProductionProxyImage]).
	CauseProxyImageNotDeclared = "imagem_do_proxy_nao_declarada"

	// Do relatório citado.

	// CauseReportMissing — o relatório citado não está ao lado do registo.
	CauseReportMissing = "relatorio_em_falta"
	// CauseReportUnreadable — o relatório não se lê, ou é maior do que o tecto.
	CauseReportUnreadable = "relatorio_ilegivel"
	// CauseReportInvalid — o relatório não confere o seu próprio digest, ou não tem a forma em que
	// o digest se calcula.
	CauseReportInvalid = "relatorio_invalido"
	// CauseReportNotCited — o relatório confere, mas o seu digest não é o que o registo cita.
	CauseReportNotCited = "relatorio_nao_e_o_citado"
	// CauseReportUnknownShape — o relatório tem uma versão ou um membro que este nó não conhece,
	// ou um valor de tipo errado num dos campos que se lêem.
	CauseReportUnknownShape = "relatorio_de_forma_desconhecida"
	// CauseReportWithoutVerdict — o relatório não é de uma qualificação: não traz veredicto.
	CauseReportWithoutVerdict = "relatorio_sem_veredicto"
	// CauseVerdictNotForProduction — o veredicto não vale para produção: a corrida não foi no
	// modo `real`, ou o relatório diz ele próprio que não vale.
	CauseVerdictNotForProduction = "veredicto_nao_vale_para_producao"
	// CauseNotQualified — o veredicto não é `qualificado`.
	CauseNotQualified = "veredicto_nao_qualificado"
	// CauseVerdictOfOtherProfile — o relatório qualifica outro perfil.
	CauseVerdictOfOtherProfile = "veredicto_de_outro_perfil"
	// CauseVerdictOfOtherProxyImage — a qualificação correu atrás de outra imagem do proxy.
	CauseVerdictOfOtherProxyImage = "veredicto_de_outra_imagem_do_proxy"

	// Do conjunto.

	// CauseNameNotAllowlisted — a allowlist assinada não autoriza o nome pedido para o board e a
	// região do nó. O perfil não autoriza um nome: só a allowlist o faz.
	CauseNameNotAllowlisted = "nome_fora_da_allowlist"
	// CauseDuplicateName — dois registos para o mesmo nome pedido.
	CauseDuplicateName = "nome_pedido_repetido"
)

// Causes devolve o vocabulário fechado das causas, pela ordem em que as conferências correm.
func Causes() []string {
	return []string{
		CauseNoTrustAnchor, CauseBadTrustAnchor, CauseSharedTrustAnchor, CauseDirUnreadable, CauseUnknownFile, CauseTooManyEntries,
		CauseSignatureMissing, CauseSignatureInvalid,
		CauseEntryUnreadable, CauseUnknownFormat, CauseShareOutOfRange, CauseBadProfile, CauseBadRouteName,
		CauseProfileDigestMismatch, CauseProfileReturnsState, CauseOtherEnvironment, CauseProxyImageNotDeclared,
		CauseReportMissing, CauseReportUnreadable, CauseReportInvalid, CauseReportNotCited, CauseReportUnknownShape,
		CauseReportWithoutVerdict, CauseVerdictNotForProduction, CauseNotQualified, CauseVerdictOfOtherProfile, CauseVerdictOfOtherProxyImage,
		CauseNameNotAllowlisted, CauseDuplicateName,
	}
}

// ErrRefused — um registo de entrada, ou a pasta deles, foi recusado. Todo o [*Refusal] casa com
// este erro por [errors.Is].
var ErrRefused = errors.New("routeentry: registo de entrada de perfil de rota recusado")

// Refusal é uma recusa: a causa, em vocabulário fechado, e o rótulo do registo a que diz respeito
// (vazio numa recusa da pasta). Não transporta conteúdo de ficheiro nenhum: o rótulo é o nome do
// ficheiro, já conferido contra o alfabeto dos rótulos ([validLabel]).
type Refusal struct {
	// Cause é uma das constantes Cause*.
	Cause string
	// Label é o rótulo do registo; vazio quando a recusa é da pasta ou da âncora.
	Label string
}

// Error implementa error. Só leva a causa e o rótulo.
func (r *Refusal) Error() string {
	if r.Label == "" {
		return fmt.Sprintf("%v: causa=%s", ErrRefused, r.Cause)
	}
	return fmt.Sprintf("%v: causa=%s registo=%s", ErrRefused, r.Cause, r.Label)
}

// Unwrap liga a recusa a [ErrRefused].
func (r *Refusal) Unwrap() error { return ErrRefused }

// refuse constrói uma recusa.
func refuse(cause, label string) error { return &Refusal{Cause: cause, Label: label} }

// CauseOf devolve a causa de um erro de [LoadDir], [ParseEntry] ou [CheckReport]; vazio se o erro
// não for uma recusa.
func CauseOf(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Cause
	}
	return ""
}
