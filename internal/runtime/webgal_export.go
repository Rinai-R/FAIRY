package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Rinai-R/FAIRY/internal/app"
)

const webGALEntryFile = "start.txt"

var webGALLabelUnsafeChars = regexp.MustCompile(`[^a-zA-Z0-9_\-]`)

func (r *Runtime) ExportWebGAL(_ context.Context, req app.WebGALExportRequest) (app.WebGALExportResponse, error) {
	if err := validateWebGALExportRequest(req); err != nil {
		return app.WebGALExportResponse{}, err
	}
	script := compileWebGAL(req)
	return app.WebGALExportResponse{
		EntryFile: webGALEntryFile,
		Script:    script,
		Files: map[string]string{
			webGALEntryFile: script,
		},
	}, nil
}

func validateWebGALExportRequest(req app.WebGALExportRequest) error {
	if strings.TrimSpace(req.Scene.ID) == "" {
		return errors.New("scene.id 不能为空")
	}
	if strings.TrimSpace(req.Scene.Title) == "" {
		return errors.New("scene.title 不能为空")
	}
	if len(req.Characters) != 1 {
		return fmt.Errorf("当前 WebGAL 导出只支持 1 个角色，收到 %d 个", len(req.Characters))
	}
	if strings.TrimSpace(req.Characters[0].ID) == "" {
		return errors.New("characters[0].id 不能为空")
	}
	if strings.TrimSpace(req.OpeningMessage) == "" {
		return errors.New("opening_message 不能为空")
	}
	if req.Interaction.Mode != "" && req.Interaction.Mode != "dialogue" && req.Interaction.Mode != "choice" {
		return fmt.Errorf("interaction.mode 只支持 dialogue 或 choice: %s", req.Interaction.Mode)
	}
	if len(req.Workflow.Nodes) > 0 && strings.TrimSpace(req.Workflow.CurrentNodeID) != "" {
		found := false
		for _, node := range req.Workflow.Nodes {
			if node.ID == req.Workflow.CurrentNodeID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("workflow.current_node_id 不存在: %s", req.Workflow.CurrentNodeID)
		}
	}
	return nil
}

func compileWebGAL(req app.WebGALExportRequest) string {
	character := req.Characters[0]
	speaker := webGALText(firstNonEmpty(character.DisplayName, character.ID))
	background := webGALResource(firstNonEmpty(req.Scene.Variables["background_url"], req.Scene.Variables["background"], character.Assets.BackgroundURL, "fairy-classroom.webp"))
	figure := webGALResource(firstNonEmpty(character.Assets.PortraitURL, character.Assets.ReferenceImageURL, character.AvatarURL, "fairy-character.webp"))
	topic := webGALText(firstNonEmpty(req.Scene.Variables["topic"], req.Scene.Title))
	goal := webGALText(req.Scene.Variables["learning_goal"])
	opening := webGALText(req.OpeningMessage)

	lines := []string{
		"; FAIRY WebGAL export",
		"changeBg:" + background + ";",
		"changeFigure:" + figure + " -center;",
		speaker + ":欢迎来到「" + topic + "」。;",
	}
	if goal != "" {
		lines = append(lines, speaker+":本轮目标是："+goal+";")
	}
	lines = append(lines, speaker+":"+opening+";")

	if len(req.Workflow.Nodes) > 0 {
		lines = append(lines, compileWorkflowNodes(req.Workflow, speaker)...)
		lines = append(lines, "; end")
		return strings.Join(lines, "\n") + "\n"
	}

	if req.Interaction.Mode == "choice" && len(req.Interaction.Choices) > 0 {
		choices := make([]string, 0, len(req.Interaction.Choices))
		for index, choice := range req.Interaction.Choices {
			if strings.TrimSpace(choice.Label) == "" || strings.TrimSpace(choice.Text) == "" {
				continue
			}
			label := webGALChoiceLabel(choice.ID, index)
			choices = append(choices, webGALText(choice.Label)+":"+label)
		}
		if len(choices) > 0 {
			lines = append(lines, "choose:"+strings.Join(choices, "|")+";")
			lines = append(lines, ";")
			for index, choice := range req.Interaction.Choices {
				if strings.TrimSpace(choice.Label) == "" || strings.TrimSpace(choice.Text) == "" {
					continue
				}
				label := webGALChoiceLabel(choice.ID, index)
				lines = append(lines,
					"label:"+label+";",
					speaker+":"+webGALText(choice.Text)+";",
					"; free-discussion:"+webGALText(choice.ID),
					speaker+":这里会进入 FAIRY 的自由讨论，你可以用自己的话继续追问或反驳。;",
					"jumpLabel:workflow_summary;",
					";",
				)
			}
			lines = append(lines, "label:workflow_summary;")
		}
	}

	lines = append(lines,
		speaker+":先到这里。接下来我们会根据你的选择，把材料拆成更适合理解的步骤。;",
		"; end",
	)
	return strings.Join(lines, "\n") + "\n"
}

func compileWorkflowNodes(workflow app.TeachingWorkflow, defaultSpeaker string) []string {
	lines := []string{";", "; workflow:" + webGALText(workflow.ID)}
	audioByNode := workflowAudioByNode(workflow.History)
	for _, node := range workflow.Nodes {
		label := webGALChoiceLabel(node.ID, 0)
		speaker := webGALText(firstNonEmpty(node.Speaker, defaultSpeaker))
		audio := audioByNode[node.ID]
		lines = append(lines,
			"label:"+label+";",
			"; node-kind:"+webGALText(node.Kind),
		)
		if audio != "" {
			lines = append(lines, "; audio:"+webGALResource(audio))
		}
		if node.BackgroundURL != "" {
			lines = append(lines, "changeBg:"+webGALResource(node.BackgroundURL)+";")
		}
		if len(node.Lines) > 0 {
			for _, line := range node.Lines {
				text := strings.TrimSpace(firstNonEmpty(line.Text, line.SpeechText))
				if text == "" {
					continue
				}
				lineSpeaker := webGALText(firstNonEmpty(line.Speaker, node.Speaker, defaultSpeaker))
				lines = append(lines, lineSpeaker+":"+webGALText(text)+webGALVocalSuffix(line.Audio.URL)+";")
			}
		} else if node.Line != "" {
			lines = append(lines, speaker+":"+webGALText(node.Line)+webGALVocalSuffix(audio)+";")
		}
		if node.Challenge != "" {
			lines = append(lines, speaker+":"+webGALText(node.Challenge)+webGALVocalSuffix(audio)+";")
		}
		if len(node.Choices) > 0 {
			choices := make([]string, 0, len(node.Choices))
			for index, choice := range node.Choices {
				if strings.TrimSpace(choice.Label) == "" {
					continue
				}
				target := webGALChoiceLabel(firstNonEmpty(node.NextNodeID, choice.ID), index)
				choices = append(choices, webGALText(choice.Label)+":"+target)
			}
			if len(choices) > 0 {
				lines = append(lines, "choose:"+strings.Join(choices, "|")+";")
			}
		}
		if node.FreeDiscussion {
			lines = append(lines, "; free-discussion:"+webGALText(node.ID))
		}
		if node.NextNodeID != "" && len(node.Choices) == 0 {
			lines = append(lines, "jumpLabel:"+webGALChoiceLabel(node.NextNodeID, 0)+";")
		}
		lines = append(lines, ";")
	}
	return lines
}

func workflowAudioByNode(history []app.WorkflowHistoryItem) map[string]string {
	out := map[string]string{}
	for _, item := range history {
		if strings.TrimSpace(item.NodeID) != "" && strings.TrimSpace(item.AudioURL) != "" {
			out[item.NodeID] = item.AudioURL
		}
	}
	return out
}

func webGALChoiceLabel(id string, index int) string {
	id = strings.TrimSpace(id)
	if id == "" {
		id = fmt.Sprintf("choice_%d", index+1)
	}
	id = strings.ReplaceAll(id, " ", "_")
	id = webGALLabelUnsafeChars.ReplaceAllString(id, "_")
	id = strings.Trim(id, "_-")
	if id == "" {
		return fmt.Sprintf("choice_%d", index+1)
	}
	return id
}

func webGALText(value string) string {
	value = strings.TrimSpace(value)
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		":", "\\:",
		";", "\\;",
		"|", "\\|",
		"\r\n", " ",
		"\n", " ",
		"\r", " ",
	)
	return replacer.Replace(value)
}

func webGALResource(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return filepath.Base(value)
	}
	return value
}

func webGALVocalSuffix(value string) string {
	resource := webGALResource(value)
	if resource == "" {
		return ""
	}
	return " -vocal=" + resource
}
