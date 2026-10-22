package routeentry

import modelgateway "github.com/aos-ref/platform/model-gateway"

// O VEREDICTO QUE UM REGISTO CITA.
//
// O relatório de qualificação é produzido pelo banco de ensaio (AOS-518), noutro computador. O nó
// não importa o banco: lê do relatório, pela leitura fechada, só o que precisa para decidir — a
// versão, o modo da corrida, a experiência, o veredicto, se vale para produção, e o perfil e a
// imagem do proxy a que diz respeito. As contagens e a regra ficam por ler: o nó não refaz o
// veredicto, confere o que o dono assinou.
//
// O CONJUNTO DE NOMES É FECHADO nos três objectos por onde a leitura passa (o topo, o bloco da
// qualificação e a rota qualificada). Um membro que este nó não conhece recusa
// ([CauseReportUnknownShape]) — é o lado fechado: um relatório de um banco mais novo não é lido
// «no que se percebe». Quem prende as listas abaixo ao que o banco escreve é um teste do banco,
// que passa um relatório verdadeiro por [ReadVerdict].

// ReportVersion é a versão do formato do relatório que este leitor conhece.
const ReportVersion = "1"

// Os valores que o nó exige. São os do banco (corrida.go e qualificacao.go), escritos aqui porque
// o nó não o importa; o teste do banco confere que coincidem.
const (
	// ModeReal é o modo de uma corrida contra o modelo real.
	ModeReal = "real"
	// ExperimentQualification é a experiência que dá um veredicto sobre a rota.
	ExperimentQualification = "qualificacao"
	// VerdictQualified é o único veredicto que deixa um perfil entrar.
	VerdictQualified = "qualificado"
	// ExperimentOCR é a experiência cujo relatório um registo de uso [UseTool] cita (AOS-530,
	// ADR-042 §2.3): o banco mede a transcrição de imagens e dá um veredicto contra o limiar de
	// factos do dono.
	ExperimentOCR = "ocr"
	// ExperimentVideo é a experiência cujo relatório um registo de uso [UseTool] com um perfil de
	// vídeo cita (AOS-536): o banco mede a descrição de vídeos sintéticos (AOS-537) e dá um
	// veredicto contra o mesmo limiar de factos.
	ExperimentVideo = "video"
	// VideoReportVersion é a versão do relatório `video` que este leitor conhece: a 2, da revisão
	// do AOS-537 (os factos de contagem e de cor lidos das linhas rotuladas). Um relatório `video` da
	// versão 1 mediu com a regra de antes, e não qualifica um perfil.
	VideoReportVersion = "2"
)

// Os membros que se lêem.
const (
	reportFieldVersion       = "versao_do_relatorio"
	reportFieldMode          = "modo"
	reportFieldExperiment    = "experiencia"
	reportFieldQualification = "qualificacao_do_modelo"
	qualFieldVerdict         = "veredicto"
	qualFieldForProduction   = "vale_para_producao"
	qualFieldRoute           = "rota_qualificada"
	routeFieldProfileDigest  = "digest_do_perfil"
	routeFieldProxyDigest    = "digest_da_imagem_do_proxy"
)

// reportTopFields são os membros do topo de um relatório da versão [ReportVersion].
var reportTopFields = []string{
	reportFieldVersion, "ticket", "data_utc", reportFieldMode, reportFieldExperiment, "terminou", "rota",
	"regiao_de_processamento_declarada", "protocolo", "digests", "plano", "pedidos", "custo", "sonda", "taxas",
	"por_braco", "por_caso", "comparacoes", "limites", "observacoes", "forma_no_fornecedor",
	"qualificacao_da_devolucao", reportFieldQualification, ReportDigestField,
}

// qualificationFields são os membros do bloco `qualificacao_do_modelo`.
var qualificationFields = []string{
	qualFieldVerdict, "razoes", "avisos", "regra", qualFieldForProduction, "porque_nao_vale_para_producao",
	"limiares", "contagens", qualFieldRoute, "controlo_sem_devolucao", "orcamento", "nao_prova",
}

// qualifiedRouteFields são os membros do bloco `rota_qualificada`.
var qualifiedRouteFields = []string{
	"fornecedor", "modelo", "modelo_esperado", "digest_da_rota", routeFieldProfileDigest,
	"digest_da_bateria", "versao_da_bateria", "imagem_do_proxy", routeFieldProxyDigest,
}

// ReportTopFields, QualificationFields e QualifiedRouteFields devolvem cópias das listas fechadas
// de nomes, para o teste do banco as comparar com o que ele escreve.
func ReportTopFields() []string      { return append([]string(nil), reportTopFields...) }
func QualificationFields() []string  { return append([]string(nil), qualificationFields...) }
func QualifiedRouteFields() []string { return append([]string(nil), qualifiedRouteFields...) }

// O RELATÓRIO DA EXPERIÊNCIA `ocr` (AOS-530). Os mesmos princípios: o nó lê só o que decide — a
// versão, o modo, a experiência, o veredicto, se vale para produção, e o perfil e a imagem do
// proxy da rota medida —, por listas FECHADAS de nomes, e não refaz o veredicto. As contagens das
// imagens ficam por ler.
const (
	ocrFieldVerdictBlock  = "veredicto_do_ocr"
	ocrFieldMeasuredRoute = "rota_medida"
)

// ocrReportTopFields são os membros do topo de um relatório `ocr` da versão [ReportVersion].
var ocrReportTopFields = []string{
	reportFieldVersion, reportFieldExperiment, "data_utc", "versao_bateria", "digest_bateria", "motor", "descricao_motor",
	"limites", "interrompida", "paragem", "imagens", "resumo", "limites_da_medicao", reportFieldMode, "rota", "pedidos",
	"custo", ocrFieldVerdictBlock, ReportDigestField,
}

// ocrVerdictFields são os membros do bloco `veredicto_do_ocr`.
var ocrVerdictFields = []string{
	qualFieldVerdict, "razoes", "regra", "limiar_de_factos", "imagens_nas_contas", "imagens_que_falharam", "factos",
	"presentes_pela_regra", qualFieldForProduction, "porque_nao_vale_para_producao", ocrFieldMeasuredRoute,
}

// ocrMeasuredRouteFields são os membros do bloco `rota_medida`.
var ocrMeasuredRouteFields = []string{
	"fornecedor", "modelo", "modelo_esperado", routeFieldProfileDigest, "imagem_do_proxy", routeFieldProxyDigest,
}

// OCRReportTopFields, OCRVerdictFields e OCRMeasuredRouteFields devolvem cópias das listas fechadas
// do relatório `ocr`, para o teste do banco as comparar com o que ele escreve.
func OCRReportTopFields() []string     { return append([]string(nil), ocrReportTopFields...) }
func OCRVerdictFields() []string       { return append([]string(nil), ocrVerdictFields...) }
func OCRMeasuredRouteFields() []string { return append([]string(nil), ocrMeasuredRouteFields...) }

// O RELATÓRIO DA EXPERIÊNCIA `video` (AOS-536). Os mesmos princípios do `ocr`, com as listas do
// relatório que o banco escreve desde a versão [VideoReportVersion]. A rota medida tem os mesmos
// membros da do `ocr` (o banco escreve-a com o mesmo tipo).
const videoFieldVerdictBlock = "veredicto_do_video"

// videoReportTopFields são os membros do topo de um relatório `video` da versão [VideoReportVersion].
var videoReportTopFields = []string{
	reportFieldVersion, reportFieldExperiment, "data_utc", "versao_bateria", "digest_bateria", "motor", "descricao_motor",
	"codificador", "instrucao", "limites", "interrompida", "paragem", "videos", "por_tipo_de_facto", "limites_da_medicao",
	reportFieldMode, "rota", "pedidos", "custo", videoFieldVerdictBlock, ReportDigestField,
}

// videoVerdictFields são os membros do bloco `veredicto_do_video`.
var videoVerdictFields = []string{
	qualFieldVerdict, "razoes", "regra", "limiar_de_factos", "videos_nas_contas", "videos_que_falharam", "factos",
	"presentes_pela_regra", qualFieldForProduction, "porque_nao_vale_para_producao", ocrFieldMeasuredRoute,
}

// VideoReportTopFields e VideoVerdictFields devolvem cópias das listas fechadas do relatório
// `video`, para o teste do banco as comparar com o que ele escreve. A rota medida compara-se com
// [OCRMeasuredRouteFields].
func VideoReportTopFields() []string { return append([]string(nil), videoReportTopFields...) }
func VideoVerdictFields() []string   { return append([]string(nil), videoVerdictFields...) }

// mediaReport descreve a forma do relatório de uma experiência de media (`ocr`, `video`): a
// experiência, a versão, as listas fechadas e o nome do bloco do veredicto.
type mediaReport struct {
	experiment, version, verdictBlock string
	top, verdict, route               []string
}

var (
	ocrReport   = mediaReport{ExperimentOCR, ReportVersion, ocrFieldVerdictBlock, ocrReportTopFields, ocrVerdictFields, ocrMeasuredRouteFields}
	videoReport = mediaReport{ExperimentVideo, VideoReportVersion, videoFieldVerdictBlock, videoReportTopFields, videoVerdictFields, ocrMeasuredRouteFields}
)

// Verdict é o que o nó lê de um relatório de qualificação.
type Verdict struct {
	// Digest é o digest do relatório, conferido contra o seu conteúdo.
	Digest string
	// Mode é o modo da corrida (`falso`, `proxy` ou `real`).
	Mode string
	// Verdict é o veredicto sobre a rota.
	Verdict string
	// ValidForProduction é o que o relatório diz de si: só uma corrida do modo `real` vale.
	ValidForProduction bool
	// ProfileDigest é o digest do perfil que a corrida qualificou.
	ProfileDigest string
	// ProxyImageDigest é o digest da imagem do proxy atrás da qual a corrida foi feita; vazio
	// quando o relatório não o traz (o modo `falso` não tem proxy).
	ProxyImageDigest string
}

// ReadVerdict confere o digest de um relatório e lê dele o veredicto, pela leitura fechada.
// Devolve um [*Refusal]: [CauseReportInvalid] quando o digest não confere, [CauseReportUnknownShape]
// quando a versão, um nome ou um tipo não são os conhecidos, e [CauseReportWithoutVerdict] quando o
// relatório não é de uma qualificação.
//
// NÃO decide se o veredicto serve: isso é de [CheckReport], que o compara com o registo assinado.
func ReadVerdict(raw []byte) (Verdict, error) {
	digest, err := VerifyReportDigest(raw)
	if err != nil {
		return Verdict{}, refuse(CauseReportInvalid, "")
	}
	top, ok := strictObject(raw)
	if !ok || !onlyKeys(top, reportTopFields) {
		return Verdict{}, refuse(CauseReportUnknownShape, "")
	}
	version, okV := textOf(top[reportFieldVersion])
	mode, okM := textOf(top[reportFieldMode])
	experiment, okE := textOf(top[reportFieldExperiment])
	if !okV || !okM || !okE || version != ReportVersion {
		return Verdict{}, refuse(CauseReportUnknownShape, "")
	}
	block, has := top[reportFieldQualification]
	if !has || experiment != ExperimentQualification {
		return Verdict{}, refuse(CauseReportWithoutVerdict, "")
	}
	qual, ok := strictObject(block)
	if !ok || !onlyKeys(qual, qualificationFields) {
		return Verdict{}, refuse(CauseReportUnknownShape, "")
	}
	v := Verdict{Digest: digest, Mode: mode}
	var okVerdict, okFor bool
	v.Verdict, okVerdict = textOf(qual[qualFieldVerdict])
	v.ValidForProduction, okFor = boolOf(qual[qualFieldForProduction])
	route, okRoute := strictObject(qual[qualFieldRoute])
	if !okVerdict || !okFor || !okRoute || !onlyKeys(route, qualifiedRouteFields) {
		return Verdict{}, refuse(CauseReportUnknownShape, "")
	}
	var okProfile bool
	if v.ProfileDigest, okProfile = textOf(route[routeFieldProfileDigest]); !okProfile {
		return Verdict{}, refuse(CauseReportUnknownShape, "")
	}
	if proxy, has := route[routeFieldProxyDigest]; has {
		var okProxy bool
		if v.ProxyImageDigest, okProxy = textOf(proxy); !okProxy {
			return Verdict{}, refuse(CauseReportUnknownShape, "")
		}
	}
	return v, nil
}

// ReadOCRVerdict confere o digest de um relatório da experiência `ocr` e lê dele o veredicto, pela
// leitura fechada (AOS-530). As causas são as de [ReadVerdict], mais
// [CauseReportOfOtherExperiment] quando o relatório não é da experiência `ocr`. Um relatório com a
// forma do AOS-529 (anterior ao veredicto) é [CauseReportInvalid] — não traz o digest de si
// próprio — ou, se alguém lho acrescentar, [CauseReportUnknownShape] — não traz a versão. Um
// relatório `ocr` com a versão e sem o bloco do veredicto é [CauseReportWithoutVerdict]; um sem `modo` (os motores
// que não são um modelo pago) lê-se com o modo vazio, que não vale para produção; um sem
// `rota_medida` lê-se sem perfil, que não é o de registo nenhum.
//
// NÃO decide se o veredicto serve: isso é de [CheckReport].
func ReadOCRVerdict(raw []byte) (Verdict, error) { return readMediaVerdict(raw, ocrReport) }

// ReadVideoVerdict é o [ReadOCRVerdict] da experiência `video` (AOS-536): as mesmas causas e a
// mesma ordem, com a versão [VideoReportVersion] e as listas fechadas do relatório `video`. Um
// relatório `ocr` (ou de qualificação) é [CauseReportOfOtherExperiment]; um `video` da versão 1, ou
// com um membro que este nó não conhece, [CauseReportUnknownShape].
func ReadVideoVerdict(raw []byte) (Verdict, error) { return readMediaVerdict(raw, videoReport) }

// readMediaVerdict é a leitura fechada do relatório de uma experiência de media.
func readMediaVerdict(raw []byte, shape mediaReport) (Verdict, error) {
	digest, err := VerifyReportDigest(raw)
	if err != nil {
		return Verdict{}, refuse(CauseReportInvalid, "")
	}
	top, ok := strictObject(raw)
	if !ok {
		return Verdict{}, refuse(CauseReportUnknownShape, "")
	}
	experiment, okE := textOf(top[reportFieldExperiment])
	if !okE {
		return Verdict{}, refuse(CauseReportUnknownShape, "")
	}
	if experiment != shape.experiment {
		return Verdict{}, refuse(CauseReportOfOtherExperiment, "")
	}
	version, okV := textOf(top[reportFieldVersion])
	if !onlyKeys(top, shape.top) || !okV || version != shape.version {
		return Verdict{}, refuse(CauseReportUnknownShape, "")
	}
	v := Verdict{Digest: digest}
	if raw, has := top[reportFieldMode]; has {
		var okM bool
		if v.Mode, okM = textOf(raw); !okM {
			return Verdict{}, refuse(CauseReportUnknownShape, "")
		}
	}
	block, has := top[shape.verdictBlock]
	if !has {
		return Verdict{}, refuse(CauseReportWithoutVerdict, "")
	}
	qual, ok := strictObject(block)
	if !ok || !onlyKeys(qual, shape.verdict) {
		return Verdict{}, refuse(CauseReportUnknownShape, "")
	}
	var okVerdict, okFor bool
	v.Verdict, okVerdict = textOf(qual[qualFieldVerdict])
	v.ValidForProduction, okFor = boolOf(qual[qualFieldForProduction])
	if !okVerdict || !okFor {
		return Verdict{}, refuse(CauseReportUnknownShape, "")
	}
	if rawRoute, has := qual[ocrFieldMeasuredRoute]; has {
		route, okRoute := strictObject(rawRoute)
		if !okRoute || !onlyKeys(route, shape.route) {
			return Verdict{}, refuse(CauseReportUnknownShape, "")
		}
		var okProfile bool
		if v.ProfileDigest, okProfile = textOf(route[routeFieldProfileDigest]); !okProfile {
			return Verdict{}, refuse(CauseReportUnknownShape, "")
		}
		if proxy, has := route[routeFieldProxyDigest]; has {
			var okProxy bool
			if v.ProxyImageDigest, okProxy = textOf(proxy); !okProxy {
				return Verdict{}, refuse(CauseReportUnknownShape, "")
			}
		}
	}
	return v, nil
}

// CheckMediaReport confere o relatório da MEDIDA DE IMAGEM que um registo de uso run com `imagem`
// cita (AOS-547, ADR-043 §2.2) pelas regras de [CheckReport]: o digest do relatório é o citado
// pelo registo assinado (antes de se ler o resto), a leitura fechada é a da experiência `ocr`
// ([ReadOCRVerdict] — o modelo do run lê as imagens da bateria de OCR), a corrida é do modo real e
// vale para produção, o veredicto é `qualificado`, o perfil medido é o do registo e a imagem do
// proxy é a citada. Um registo sem a medida nunca chega aqui: [ParseEntry] já o recusou.
func CheckMediaReport(e Entry, report []byte) error {
	digest, err := VerifyReportDigest(report)
	if err != nil {
		return refuse(CauseReportInvalid, "")
	}
	if e.MediaReportDigest == "" || digest != e.MediaReportDigest {
		return refuse(CauseReportNotCited, "")
	}
	v, err := ReadOCRVerdict(report)
	if err != nil {
		return err
	}
	return conferirVeredicto(e, v)
}

// conferirVeredicto são os passos 4 a 7 de [CheckReport], partilhados com [CheckMediaReport].
func conferirVeredicto(e Entry, v Verdict) error {
	if v.Mode != ModeReal || !v.ValidForProduction {
		return refuse(CauseVerdictNotForProduction, "")
	}
	if v.Verdict != VerdictQualified {
		return refuse(CauseNotQualified, "")
	}
	if v.ProfileDigest != e.ProfileDigest {
		return refuse(CauseVerdictOfOtherProfile, "")
	}
	if v.ProxyImageDigest != e.ProxyImageDigest {
		return refuse(CauseVerdictOfOtherProxyImage, "")
	}
	return nil
}

// CheckReport confere o relatório citado contra o registo de entrada. A ORDEM é fixa:
//
//  1. o relatório confere o seu próprio digest;
//  2. esse digest é o que o registo ASSINADO cita — é este passo que dá autenticidade ao que se
//     lê a seguir, e por isso vem ANTES de se ler seja o que for do relatório;
//  3. o relatório lê-se pela leitura fechada: [ReadVerdict] para um registo de uso [UseRun] — a
//     regra de sempre, sem mudança —; para um de uso [UseTool], a leitura da media do perfil:
//     [ReadOCRVerdict] para `imagem`, que só aceita um relatório da experiência `ocr` (AOS-530,
//     ADR-042 §2.3), e [ReadVideoVerdict] para `video`, que só aceita um da experiência `video`
//     (AOS-536);
//  4. a corrida foi no modo `real` e o relatório diz que vale para produção;
//  5. o veredicto é `qualificado`;
//  6. o perfil qualificado é o do registo;
//  7. a imagem do proxy da corrida é a que o registo cita.
//
// Devolve um [*Refusal] sem rótulo; [LoadDir] põe-lho.
func CheckReport(e Entry, report []byte) error {
	digest, err := VerifyReportDigest(report)
	if err != nil {
		return refuse(CauseReportInvalid, "")
	}
	if digest != e.ReportDigest {
		return refuse(CauseReportNotCited, "")
	}
	read := ReadVerdict
	switch {
	case e.ForTool() && e.Media == modelgateway.CapabilityVideo:
		read = ReadVideoVerdict
	case e.ForTool():
		read = ReadOCRVerdict
	}
	v, err := read(report)
	if err != nil {
		return err
	}
	return conferirVeredicto(e, v)
}
