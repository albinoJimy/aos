package bancoensaio

import (
	"context"
	"io"
)

// PREPARAR O REGISTO DE USO `tool` DE IMAGEM (AOS-530, ADR-042 §2.3).
//
// `aos-ensaio qualificar-ocr` é o `qualificar` do AOS-522 para o alias de uma tool cujo efeito é
// uma chamada a um modelo de visão (a `ocr_read`): corre a experiência `ocr` com o perfil do alias
// (`--perfil`, capacidade `imagem` e sem `tools`) e, só com o veredicto `qualificado` de uma
// corrida completa do modo real, deixa pronto o registo de uso `tool` por assinar. O caminho, as
// barreiras e as saídas são os de [qualificarMedia] (qualificar_media.go), partilhados com o
// `qualificar-video` (AOS-536).

const usoDoQualificarOCR = `aos-ensaio qualificar-ocr — mede o alias de uma tool de OCR e prepara o registo de entrada de uso tool para o dono assinar (AOS-530)

  aos-ensaio qualificar-ocr --motor visao --chaves F --visao-fornecedor kimi|anthropic|openrouter [--visao-modelo M]
                            --perfil F --board B --regiao R --saida PASTA [opcoes]
  aos-ensaio qualificar-ocr --motor falso [--falso certo|sem-acentos|falha-pdf|erra-factos]
                            --perfil F --board B --regiao R --saida PASTA

  --perfil FICHEIRO   o perfil do alias da tool: capacidade imagem, sem tools; o seu "requested" e o alias
                      que o no chama pela tool
  --board B           o board do no a que o registo se destina   (sem valor por omissao)
  --regiao R          a regiao desse no                          (sem valor por omissao)
  --saida PASTA       pasta que AINDA NAO EXISTE: e criada por esta corrida, e leva o relatorio, o resumo
                      e, so com veredicto positivo do modo real, pacote/
  --allowlist-dir PASTA --ancora-da-allowlist BASE64
                      a allowlist externa do no, se ele montar uma; sem elas confere-se a embebida
  opcoes: --limiar-de-factos F (de 0.90 a 1; so aperta)  --precos F  --reconstruir-contador
          --guardar-respostas

O registo e de uso tool, com fatia 0: um registo de uso tool nunca e candidato a servir runs. So um
qualificado do motor visao (o modo real), com a corrida completa, deixa um registo por assinar. O
comando NAO assina e nao le chave de assinar nenhuma: assina o dono, com o aos-issuer route-profile-sign.
Saidas: 0 pacote preparado; 7 recusado; 9 qualificado com o motor falso (nada para assinar);
        11 o no recusaria o registo; 4 parou a meio; 3 recusada antes de enviar; 5 em CI; 6 sem infra;
        2 argumentos; 1 outra falha (nunca fica pacote).
`

// qualificacaoDoOCR é o `qualificar-ocr`: a experiência `ocr`.
var qualificacaoDoOCR = qualificacaoDeMedia{
	comando:   "qualificar-ocr",
	uso:       usoDoQualificarOCR,
	cabecalho: "REGISTO DE ENTRADA DE USO TOOL (AOS-530, ADR-042):",
	lerPerfil: lerPerfilDoOCR,
	correr:    correrOCRParaQualificar,
}

// qualificarOCR é o subcomando `qualificar-ocr`.
func qualificarOCR(ctx context.Context, args []string, stdout, stderr io.Writer, amb Ambiente, red *Redactor) int {
	return qualificarMedia(ctx, qualificacaoDoOCR, args, stdout, stderr, amb, red)
}

// correrOCRParaQualificar corre a experiência `ocr` e devolve o que ela escreveu, lido do relatório
// EM MEMÓRIA ([ocrFeito]) — e não de um relatório de fora.
func correrOCRParaQualificar(ctx context.Context, args []string, stderr io.Writer, amb Ambiente, red *Redactor) (int, *medidaDeMedia) {
	var feito ocrFeito
	codigo := executarOCRCom(ctx, args, io.Discard, stderr, amb, red, &feito)
	r := feito.relatorio
	if r == nil || r.VeredictoDoOCR == nil {
		return codigo, nil
	}
	v := r.VeredictoDoOCR
	return codigo, &medidaDeMedia{
		emJSON: feito.emJSON, digest: r.DigestDoRelatorio, motor: r.Motor, modo: r.Modo, interrompida: r.Interrompida,
		veredicto: v.Veredicto, vale: v.ValeParaProducao, porqueNaoValeParaProducao: v.PorqueNaoValeParaProducao, rota: v.RotaMedida,
		resumo: ResumoOCREmTexto(r),
	}
}
