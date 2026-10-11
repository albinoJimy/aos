package bancoensaio

import (
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
)

// O DIGEST DO PRÓPRIO RELATÓRIO (AOS-518).
//
// Um relatório de qualificação traz, no campo `digest_do_relatorio` do topo, o sha256 de si
// próprio. É o que um perfil assinado cita e o que o nó confere (AOS-519).
//
// ONDE VIVE O CÁLCULO. Desde o AOS-519, no pacote `routeentry` do Model Gateway
// ([routeentry.CanonicalReport]): o banco escreve o digest e o nó confere-o, e o nó não importa
// código de ensaio. As funções abaixo delegam nele, pelo que há UMA só escrita da forma canónica —
// a descrição dela, passo a passo, está ao lado do código.
//
// O QUE ISTO DÁ: mudar uma contagem, uma razão, o veredicto ou qualquer outro valor muda o digest;
// mudar só a indentação ou a ordem dos membros não muda. O resumo em texto (`.txt`) NÃO é coberto:
// é derivado do JSON, e é o JSON que se confere.
//
// O QUE NÃO DÁ: o digest dá INTEGRIDADE, não AUTENTICIDADE. Quem altera o veredicto pode
// recalcular o digest e pô-lo no ficheiro: a verificação passa. O digest serve para um registo
// ASSINADO citar um relatório exacto (AOS-519) — é a assinatura desse registo que diz quem
// responde por ele. E não prova que a corrida aconteceu: o relatório não é assinado pelo banco.

// CampoDoDigestDoRelatorio é o nome do membro do topo que guarda o digest do relatório.
const CampoDoDigestDoRelatorio = routeentry.ReportDigestField

// ErrDigestDoRelatorio — o relatório não tem a forma em que o digest se calcula, não traz digest,
// ou o digest que traz não é o seu. É o mesmo erro que o nó vê ([routeentry.ErrReportDigest]).
var ErrDigestDoRelatorio = routeentry.ErrReportDigest

// FormaCanonicaDoRelatorio devolve a forma canónica de um relatório em JSON, sem o membro do digest.
func FormaCanonicaDoRelatorio(cru []byte) ([]byte, error) {
	return routeentry.CanonicalReport(cru)
}

// DigestDoRelatorio calcula o digest de um relatório em JSON: o sha256 da sua forma canónica.
func DigestDoRelatorio(cru []byte) (string, error) {
	return routeentry.ReportDigest(cru)
}

// VerificarDigestDoRelatorio confere que o digest que o relatório traz é o da sua forma canónica.
// Devolve o digest. Um relatório sem o campo, ou com outro valor, é [ErrDigestDoRelatorio].
func VerificarDigestDoRelatorio(cru []byte) (string, error) {
	return routeentry.VerifyReportDigest(cru)
}
