package bancoensaio

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AS QUALIFICAÇÕES DO DIA (AOS-518, decisão D7): repetir até passar é escolher o resultado.
//
// Dois tectos por dia (UTC), ambos conferidos antes de enviar e contados quando a sonda passa:
//
//   - por PERFIL (o seu digest): [QualificacoesPorPerfilPorDia];
//   - por ROTA (fornecedor e modelo): [QualificacoesPorRotaPorDia] — sem ele, mudar um parâmetro
//     do perfil dava um digest novo e um contador novo.
//
// A contagem é o MAIOR de dois números: o do registo ([NomeDoRegistoDeQualificacoes], na pasta do
// dono) e o dos relatórios de qualificações reais de hoje que estiverem nas pastas de relatórios.
// Apagar só o registo não repõe a contagem.
//
// O QUE ISTO NÃO FECHA: quem tem a máquina apaga o registo E os relatórios, ou muda a pasta do
// utilizador, e recomeça. O tecto protege de enganos e de pressa, não de quem o quer contornar.

const (
	// QualificacoesPorRotaPorDia é o máximo de qualificações da mesma rota (fornecedor e modelo),
	// com qualquer perfil, no mesmo dia (UTC).
	QualificacoesPorRotaPorDia = 4
	// NomeDoRegistoDeQualificacoes é o ficheiro que conta as qualificações por dia.
	NomeDoRegistoDeQualificacoes = "qualificacoes.json"
)

// ErrQualificacoesDoDia — o perfil, ou a rota, já foi qualificado o máximo de vezes hoje.
var ErrQualificacoesDoDia = errors.New("banco-ensaio: o maximo de qualificacoes de hoje ja foi atingido para este perfil ou para esta rota (causa: qualificacoes_do_dia_esgotadas); repetir ate passar e escolher o resultado")

// ErrRegistoDeQualificacoes — o registo das qualificações do dia existe e não se lê, ou não se
// consegue escrever. Não se recomeça do zero: repara-se à mão.
var ErrRegistoDeQualificacoes = errors.New("banco-ensaio: o registo das qualificacoes do dia nao se le ou nao se escreve")

// ChaveDaQualificacao diz de que qualificação se trata na contagem do dia.
type ChaveDaQualificacao struct {
	// Dia em UTC (`2006-01-02`); PerfilDigest, o digest do perfil candidato.
	Dia, PerfilDigest string
	// Fornecedor e Modelo identificam a rota.
	Fornecedor, Modelo string
}

func (k ChaveDaQualificacao) rota() string { return k.Fornecedor + "/" + k.Modelo }

// OrdemDaQualificacao é quantas qualificações já contam hoje, pelo perfil e pela rota.
type OrdemDaQualificacao struct {
	DoPerfil, DaRota int
}

func (o OrdemDaQualificacao) esgotada() bool {
	return o.DoPerfil >= QualificacoesPorPerfilPorDia || o.DaRota >= QualificacoesPorRotaPorDia
}

type qualificacoesDeUmDia struct {
	Perfis map[string]int `json:"perfis"`
	Rotas  map[string]int `json:"rotas"`
}

type registoDeQualificacoes struct {
	// Dias: dia (UTC) → as contagens desse dia.
	Dias map[string]qualificacoesDeUmDia `json:"dias"`
}

func lerRegistoDeQualificacoes(caminho string) (registoDeQualificacoes, error) {
	r := registoDeQualificacoes{Dias: map[string]qualificacoesDeUmDia{}}
	cru, err := os.ReadFile(caminho) // #nosec G304 -- ficheiro do proprio banco, na pasta do dono
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, ErrRegistoDeQualificacoes
	}
	dec := json.NewDecoder(strings.NewReader(string(cru)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil || r.Dias == nil {
		return r, ErrRegistoDeQualificacoes
	}
	for _, d := range r.Dias {
		for _, m := range []map[string]int{d.Perfis, d.Rotas} {
			for _, n := range m {
				if n < 0 {
					return r, ErrRegistoDeQualificacoes
				}
			}
		}
	}
	return r, nil
}

// relatorioDeOCRQualificante é o que se lê de um relatório `ocr` do modo real para o contar entre
// as qualificações de hoje (AOS-530): só conta o que mediu um perfil (traz a rota medida). Um
// relatório destes só se escreve depois de a sonda passar.
type relatorioDeOCRQualificante struct {
	Modo        string `json:"modo"`
	Experiencia string `json:"experiencia"`
	DataUTC     string `json:"data_utc"`
	Rota        struct {
		Fornecedor string `json:"fornecedor"`
		Modelo     string `json:"modelo"`
	} `json:"rota"`
	Veredicto *veredictoQualificante `json:"veredicto_do_ocr"`
	// VeredictoDoVideo é o mesmo bloco num relatório `video` (AOS-537).
	VeredictoDoVideo *veredictoQualificante `json:"veredicto_do_video"`
}

type veredictoQualificante struct {
	RotaMedida *struct {
		DigestDoPerfil string `json:"digest_do_perfil"`
	} `json:"rota_medida"`
}

// ocrQualificantesNosRelatorios conta, nos relatórios `ocr` (AOS-530) e `video` (AOS-537) reais
// das pastas dadas, os que qualificaram um perfil hoje.
func ocrQualificantesNosRelatorios(pastas []string, k ChaveDaQualificacao, vistos map[string]bool) OrdemDaQualificacao {
	var o OrdemDaQualificacao
	// Desde o AOS-537 contam também as corridas `video` que mediram um perfil: o tecto é sobre a
	// rota, meça ela imagens ou vídeos.
	for _, pasta := range pastas {
		ocr, _ := filepath.Glob(filepath.Join(pasta, "ensaio-"+ModoReal+"-ocr-*.json"))
		video, _ := filepath.Glob(filepath.Join(pasta, "ensaio-"+ModoReal+"-video-*.json"))
		for _, caminho := range append(ocr, video...) {
			abs, err := filepath.Abs(caminho)
			if err != nil || vistos[abs] {
				continue
			}
			vistos[abs] = true
			cru, err := os.ReadFile(caminho) // #nosec G304 -- relatorios do proprio banco, na pasta do dono
			if err != nil {
				continue
			}
			var r relatorioDeOCRQualificante
			if json.Unmarshal(cru, &r) != nil || r.Modo != ModoReal || !strings.HasPrefix(r.DataUTC, k.Dia) {
				continue
			}
			v := r.Veredicto
			switch r.Experiencia {
			case "ocr":
			case "video":
				v = r.VeredictoDoVideo
			default:
				continue
			}
			if v == nil || v.RotaMedida == nil {
				continue
			}
			if r.Rota.Fornecedor == k.Fornecedor && r.Rota.Modelo == k.Modelo {
				o.DaRota++
			}
			if v.RotaMedida.DigestDoPerfil == k.PerfilDigest {
				o.DoPerfil++
			}
		}
	}
	return o
}

// relatorioDeQualificacao é o que se lê de um relatório para contar as qualificações de hoje.
type relatorioDeQualificacao struct {
	Modo        string `json:"modo"`
	Experiencia string `json:"experiencia"`
	DataUTC     string `json:"data_utc"`
	Terminou    string `json:"terminou"`
	Rota        struct {
		Fornecedor string `json:"fornecedor"`
		Modelo     string `json:"modelo"`
	} `json:"rota"`
	Digests struct {
		Perfil string `json:"perfil"`
	} `json:"digests"`
	Sonda *struct {
		Resultado string `json:"resultado"`
	} `json:"sonda"`
}

// qualificacoesNosRelatorios conta, nos relatórios das pastas dadas, as qualificações REAIS do dia
// que passaram da sonda — as que contam.
func qualificacoesNosRelatorios(pastas []string, k ChaveDaQualificacao) OrdemDaQualificacao {
	var o OrdemDaQualificacao
	vistos := map[string]bool{}
	for _, pasta := range pastas {
		achados, _ := filepath.Glob(filepath.Join(pasta, "ensaio-"+ModoReal+"-"+string(ExperienciaQualificacao)+"-*.json"))
		for _, caminho := range achados {
			abs, err := filepath.Abs(caminho)
			if err != nil || vistos[abs] {
				continue
			}
			vistos[abs] = true
			cru, err := os.ReadFile(caminho) // #nosec G304 -- relatorios do proprio banco, na pasta do dono
			if err != nil {
				continue
			}
			var r relatorioDeQualificacao
			if json.Unmarshal(cru, &r) != nil || r.Modo != ModoReal || r.Experiencia != string(ExperienciaQualificacao) || !strings.HasPrefix(r.DataUTC, k.Dia) {
				continue
			}
			if r.Terminou == TerminouNaoCoube || (r.Sonda != nil && r.Sonda.Resultado != SondaOK) {
				continue // nao passou da sonda: nao contou
			}
			if r.Rota.Fornecedor == k.Fornecedor && r.Rota.Modelo == k.Modelo {
				o.DaRota++
			}
			if r.Digests.Perfil == k.PerfilDigest {
				o.DoPerfil++
			}
		}
	}
	// Desde o AOS-530 contam também as corridas `ocr` que qualificaram um perfil.
	ocr := ocrQualificantesNosRelatorios(pastas, k, vistos)
	o.DoPerfil += ocr.DoPerfil
	o.DaRota += ocr.DaRota
	return o
}

// QualificacoesDeHoje devolve quantas qualificações já contam hoje para o perfil e para a rota: o
// maior entre o registo e os relatórios de hoje. Não escreve.
func QualificacoesDeHoje(caminho string, pastasDeRelatorios []string, k ChaveDaQualificacao) (OrdemDaQualificacao, error) {
	r, err := lerRegistoDeQualificacoes(caminho)
	if err != nil {
		return OrdemDaQualificacao{}, err
	}
	doRegisto := OrdemDaQualificacao{DoPerfil: r.Dias[k.Dia].Perfis[k.PerfilDigest], DaRota: r.Dias[k.Dia].Rotas[k.rota()]}
	dosRelatorios := qualificacoesNosRelatorios(pastasDeRelatorios, k)
	return OrdemDaQualificacao{DoPerfil: max(doRegisto.DoPerfil, dosRelatorios.DoPerfil), DaRota: max(doRegisto.DaRota, dosRelatorios.DaRota)}, nil
}

// RegistarQualificacao conta mais uma qualificação e devolve o seu número de ordem, pelo perfil e
// pela rota. Com um dos máximos já atingido devolve [ErrQualificacoesDoDia] e não escreve nada.
// Chama-se depois de a sonda passar e ANTES do primeiro caso, com o contador do dia aberto (é a
// trava dele que garante um processo de cada vez).
func RegistarQualificacao(caminho string, pastasDeRelatorios []string, k ChaveDaQualificacao) (OrdemDaQualificacao, error) {
	feitas, err := QualificacoesDeHoje(caminho, pastasDeRelatorios, k)
	if err != nil {
		return feitas, err
	}
	if feitas.esgotada() {
		return feitas, ErrQualificacoesDoDia
	}
	r, err := lerRegistoDeQualificacoes(caminho)
	if err != nil {
		return feitas, err
	}
	// Só o dia de hoje fica: o registo não cresce.
	hoje := r.Dias[k.Dia]
	if hoje.Perfis == nil {
		hoje.Perfis = map[string]int{}
	}
	if hoje.Rotas == nil {
		hoje.Rotas = map[string]int{}
	}
	nova := OrdemDaQualificacao{DoPerfil: feitas.DoPerfil + 1, DaRota: feitas.DaRota + 1}
	hoje.Perfis[k.PerfilDigest], hoje.Rotas[k.rota()] = nova.DoPerfil, nova.DaRota
	cru, err := json.MarshalIndent(registoDeQualificacoes{Dias: map[string]qualificacoesDeUmDia{k.Dia: hoje}}, "", "  ")
	if err != nil {
		return feitas, err
	}
	if err := os.MkdirAll(filepath.Dir(caminho), 0o700); err != nil {
		return feitas, fmt.Errorf("%w: %s", ErrRegistoDeQualificacoes, filepath.Base(caminho))
	}
	temporario := caminho + ".tmp"
	if err := os.WriteFile(temporario, append(cru, '\n'), 0o600); err != nil {
		return feitas, fmt.Errorf("%w: %s", ErrRegistoDeQualificacoes, filepath.Base(temporario))
	}
	if err := os.Rename(temporario, caminho); err != nil {
		return feitas, fmt.Errorf("%w: %s", ErrRegistoDeQualificacoes, filepath.Base(caminho))
	}
	return nova, nil
}
