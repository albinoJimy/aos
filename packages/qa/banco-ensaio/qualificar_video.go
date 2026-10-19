package bancoensaio

import (
	"context"
	"errors"
	"io"
	"slices"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
)

// PREPARAR O REGISTO DE USO `tool` DE VÍDEO (AOS-536, parte 1).
//
// `aos-ensaio qualificar-video` é o `qualificar-ocr` (AOS-530) para o alias de uma tool cujo efeito
// é uma chamada a um modelo capaz de vídeo: corre a experiência `video` (AOS-537) com o perfil do
// alias (`--perfil`, capacidade `video`, sem `imagem` nem `tools`) e, só com o veredicto
// `qualificado` de uma corrida completa do modo real, deixa pronto o registo de uso `tool` por
// assinar, a citar o relatório `video`. O caminho, as barreiras e as saídas são os de
// [qualificarMedia] (qualificar_media.go).

const usoDoQualificarVideo = `aos-ensaio qualificar-video — mede o alias de uma tool de video e prepara o registo de entrada de uso tool para o dono assinar (AOS-536)

  aos-ensaio qualificar-video --motor visao --chaves F --visao-fornecedor openrouter|kimi|anthropic [--visao-modelo M]
                              --perfil F --board B --regiao R --saida PASTA [opcoes]
  aos-ensaio qualificar-video --motor falso [--falso certo|erra-factos|cortado]
                              --perfil F --board B --regiao R --saida PASTA

  --perfil FICHEIRO   o perfil do alias da tool: capacidade video, sem imagem nem tools; o seu "requested"
                      e o alias que o no chama pela tool, e o seu "expected_model" tem de ser a rota medida
                      (<prefixo do proxy>/<modelo>, por exemplo openrouter/google/gemini-3.5-flash-lite)
  --board B           o board do no a que o registo se destina   (sem valor por omissao)
  --regiao R          a regiao desse no                          (sem valor por omissao)
  --saida PASTA       pasta que AINDA NAO EXISTE: e criada por esta corrida, e leva o relatorio, o resumo
                      e, so com veredicto positivo do modo real, pacote/
  --allowlist-dir PASTA --ancora-da-allowlist BASE64
                      a allowlist externa do no, se ele montar uma; sem elas confere-se a embebida
  opcoes: --limiar-de-factos F (de 0.90 a 1; so aperta)  --precos F  --reconstruir-contador
          --guardar-respostas

O registo e de uso tool, com fatia 0, e cita o relatorio da experiencia video: um registo de uso tool
nunca e candidato a servir runs. So um qualificado do motor visao (o modo real), com a corrida
completa, deixa um registo por assinar. Os MP4 gera-os o ffmpeg fixado do banco. O comando NAO assina
e nao le chave de assinar nenhuma: assina o dono, com o aos-issuer route-profile-sign.
Saidas: 0 pacote preparado; 7 recusado; 9 qualificado com o motor falso (nada para assinar);
        11 o no recusaria o registo; 4 parou a meio; 3 recusada antes de enviar (chaves, tecto,
        contador, qualificacoes do dia, expected_model que nao e a rota); 5 em CI; 6 sem infra
        (Docker, ffmpeg, proxy); 2 argumentos; 1 outra falha (nunca fica pacote).
`

// qualificacaoDoVideo é o `qualificar-video`: a experiência `video`.
var qualificacaoDoVideo = qualificacaoDeMedia{
	comando:   "qualificar-video",
	uso:       usoDoQualificarVideo,
	cabecalho: "REGISTO DE ENTRADA DE USO TOOL DE VIDEO (AOS-536):",
	lerPerfil: lerPerfilDoVideoTool,
	correr:    correrVideoParaQualificar,
}

// qualificarVideo é o subcomando `qualificar-video`.
func qualificarVideo(ctx context.Context, args []string, stdout, stderr io.Writer, amb Ambiente, red *Redactor) int {
	return qualificarMedia(ctx, qualificacaoDoVideo, args, stdout, stderr, amb, red)
}

// ErrPerfilDoVideoTool — o perfil dado ao `qualificar-video` não é o de uma rota que só lê vídeo.
var ErrPerfilDoVideoTool = errors.New("o perfil do registo de uso tool de video tem de declarar a capacidade " + modelgateway.CapabilityVideo +
	" e nao pode declarar " + modelgateway.CapabilityImage + " nem " + modelgateway.CapabilityTools + " (o no aceita exactamente uma capacidade de media num registo de uso tool, e o modelo que ve o video nao pode pedir efeitos)")

// lerPerfilDoVideoTool lê e confere o perfil do `qualificar-video`: o da experiência `video`
// ([lerPerfilDoVideo]), com a capacidade `video` e sem `imagem`.
func lerPerfilDoVideoTool(caminho string) (*modelgateway.RouteProfile, error) {
	p, err := lerPerfilDoVideo(caminho)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(p.Capabilities, modelgateway.CapabilityVideo) || slices.Contains(p.Capabilities, modelgateway.CapabilityImage) {
		return nil, ErrPerfilDoVideoTool
	}
	return p, nil
}

// correrVideoParaQualificar corre a experiência `video` e devolve o que ela escreveu, lido do
// relatório EM MEMÓRIA ([videoFeito]).
func correrVideoParaQualificar(ctx context.Context, args []string, stderr io.Writer, amb Ambiente, red *Redactor) (int, *medidaDeMedia) {
	var feito videoFeito
	codigo := executarVideoCom(ctx, args, io.Discard, stderr, amb, red, &feito)
	r := feito.relatorio
	if r == nil || r.VeredictoDoVideo == nil {
		return codigo, nil
	}
	v := r.VeredictoDoVideo
	return codigo, &medidaDeMedia{
		emJSON: feito.emJSON, digest: r.DigestDoRelatorio, motor: r.Motor, modo: r.Modo, interrompida: r.Interrompida,
		veredicto: v.Veredicto, vale: v.ValeParaProducao, porqueNaoValeParaProducao: v.PorqueNaoValeParaProducao, rota: v.RotaMedida,
		resumo: ResumoVideoEmTexto(r),
	}
}

// experienciaDaMedia é a experiência do banco cujo relatório um registo de uso tool com a media
// dada cita (a regra de [routeentry.CheckReport]).
func experienciaDaMedia(media string) string {
	if media == modelgateway.CapabilityVideo {
		return routeentry.ExperimentVideo
	}
	return routeentry.ExperimentOCR
}
