package edge

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fairy/context/character"
	"fairy/context/knowledge"
	"fairy/context/memory/personal"
	"fairy/runtime/config"
	"fairy/runtime/model"
	"fairy/runtime/seekdb"
	"fairy/runtime/wasm"
	"fairy/transport/session"
)

const (
	packagedVerificationPackID         = "fairy.package-verification"
	packagedVerificationEndpointKey    = "package-verification-desktop"
	packagedVerificationPreferredName  = "Endpoint Verification"
	packagedVerificationUserMessage    = "验证最终安装包的本地持久化。"
	packagedVerificationAssistantReply = "最终安装包已完成本地持久化验证。"
	packagedVerificationMemoryContent  = "最终安装包无需外部数据库即可保存个人记忆。"
	packagedVerificationKnowledgeTopic = "安装验证"
	packagedVerificationKnowledgeFact  = "最终安装包关闭后能够从进程内 SeekDB 恢复知识。"
	packagedVerificationTransparentPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
)

type packagedEndpointPersistenceFixture struct {
	characterID    string
	conversationID string
	turnID         string
	memoryID       string
	knowledgeID    string
}

// PackagedSeekDBArtifact identifies the fixed files that seal the in-process
// SeekDB release inside the Desktop App bundle.
type PackagedSeekDBArtifact struct {
	LibraryPath      string
	LicensePath      string
	NoticePath       string
	AppInfoPlistPath string
}

// LocatePackagedSeekDBLibrary resolves only the fixed App-bundle location
// owned by the runtime locator.
func LocatePackagedSeekDBLibrary() (string, error) {
	return seekdb.LocateLibrary()
}

// VerifyPackagedSeekDBArtifact keeps the Desktop surface on the public Edge
// boundary while replaying the runtime-owned artifact catalog checks.
func VerifyPackagedSeekDBArtifact(bundle PackagedSeekDBArtifact) error {
	catalog, err := seekdb.BuiltinArtifactCatalog()
	if err != nil {
		return err
	}
	return catalog.VerifyPackagedBundle("darwin", "arm64", seekdb.ArtifactBundle{
		LibraryPath:      bundle.LibraryPath,
		LicensePath:      bundle.LicensePath,
		NoticePath:       bundle.NoticePath,
		AppInfoPlistPath: bundle.AppInfoPlistPath,
	})
}

// VerifyInstalledPluginReleaseInventory validates the immutable WASM release
// inventory without exposing runtime/wasm as a Desktop surface dependency.
func VerifyInstalledPluginReleaseInventory(ctx context.Context, root string) error {
	return wasm.VerifyInstalledReleaseInventory(ctx, root)
}

// VerifyPackagedSeekDBRuntime opens the complete endpoint-strict Core/Edge
// composition from the fixed in-bundle SeekDB library. It proves that schema,
// local history/config/management, encrypted secrets, and the deny-by-default
// WASM host remain available without configuring any model provider or
// OpenSERP. Release tooling invokes this in a clean disposable helper process
// and requires a durable completion marker, because an upstream embedded-
// engine _Exit(0) must never be mistaken for success.
func VerifyPackagedSeekDBRuntime(ctx context.Context, verificationRoot string) error {
	if ctx == nil {
		return errors.New("packaged SeekDB verification context is required")
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return fmt.Errorf("packaged SeekDB runtime verification requires darwin/arm64, got %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if err := validateRuntimeVerificationRoot(verificationRoot); err != nil {
		return err
	}
	if err := installPackagedVerificationVisualPack(verificationRoot); err != nil {
		return err
	}
	first, err := OpenEndpointStrict(ctx, Options{
		ConfigRoot: verificationRoot,
		Profile:    ProfileEndpointStrict,
	})
	if err != nil {
		return fmt.Errorf("open packaged endpoint runtime: %w", err)
	}
	verifyErr := verifyPackagedEndpointRuntimeState(ctx, first, verificationRoot)
	var fixture packagedEndpointPersistenceFixture
	if verifyErr == nil {
		fixture, verifyErr = writePackagedEndpointPersistenceFixture(ctx, first)
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	closeErr := first.Close(closeCtx)
	cancel()
	if closeErr != nil {
		closeErr = fmt.Errorf("close packaged SeekDB runtime: %w", closeErr)
	}
	if err := errors.Join(verifyErr, closeErr); err != nil {
		return err
	}

	second, err := OpenEndpointStrict(ctx, Options{
		ConfigRoot: verificationRoot,
		Profile:    ProfileEndpointStrict,
	})
	if err != nil {
		return fmt.Errorf("reopen packaged endpoint runtime: %w", err)
	}
	verifyErr = errors.Join(
		verifyPackagedEndpointRuntimeState(ctx, second, verificationRoot),
		verifyPackagedEndpointPersistence(ctx, second, fixture),
	)
	closeCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	closeErr = second.Close(closeCtx)
	cancel()
	if closeErr != nil {
		closeErr = fmt.Errorf("close reopened packaged SeekDB runtime: %w", closeErr)
	}
	return errors.Join(verifyErr, closeErr)
}

func installPackagedVerificationVisualPack(root string) error {
	directory := filepath.Join(character.VisualPacksRoot(root), packagedVerificationPackID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create packaged verification visual pack: %w", err)
	}
	manifest := `{"schemaVersion":2,"packId":"` + packagedVerificationPackID + `","displayName":"Package Verification","renderer":"state_images","frame":{"width":1,"height":1},"scale":1,"anchor":{"x":0,"y":1},"states":[{"id":"idle","description":"idle","imagePath":"fairy-character://localhost/` + packagedVerificationPackID + `/idle.png"}]}`
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), []byte(manifest), 0o600); err != nil {
		return fmt.Errorf("write packaged verification visual manifest: %w", err)
	}
	png, err := base64.StdEncoding.DecodeString(packagedVerificationTransparentPNG)
	if err != nil {
		return fmt.Errorf("decode packaged verification visual asset: %w", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "idle.png"), png, 0o600); err != nil {
		return fmt.Errorf("write packaged verification visual asset: %w", err)
	}
	return nil
}

func writePackagedEndpointPersistenceFixture(ctx context.Context, instance *Runtime) (packagedEndpointPersistenceFixture, error) {
	management := instance.Management()
	if management == nil || instance.Core() == nil || instance.Facade() == nil {
		return packagedEndpointPersistenceFixture{}, errors.New("packaged endpoint persistence composition is incomplete")
	}
	created, err := instance.Core().Character.CreateCharacter(character.Brief{
		Name:             "Package Verification",
		Description:      "验证最终安装包的本地持久化边界。",
		TextLanguage:     "zh",
		SpeakingLanguage: "zh",
	}, packagedVerificationPackID)
	if err != nil {
		return packagedEndpointPersistenceFixture{}, fmt.Errorf("create packaged verification character: %w", err)
	}
	if _, err := management.ActivateCharacter(created.CharacterID, created.Revision); err != nil {
		return packagedEndpointPersistenceFixture{}, fmt.Errorf("activate packaged verification character: %w", err)
	}
	preferredName := packagedVerificationPreferredName
	if _, err := management.SaveProfile(&preferredName); err != nil {
		return packagedEndpointPersistenceFixture{}, fmt.Errorf("save packaged verification profile: %w", err)
	}
	opened, err := instance.Facade().OpenSession(ctx, session.OpenSessionRequest{
		Endpoint:    session.EndpointDesktop,
		EndpointKey: packagedVerificationEndpointKey,
		Interaction: session.Context{
			Audience:     session.AudienceSingle,
			Initiation:   session.InitiationDirect,
			Presentation: session.PresentationChat,
		},
	})
	if err != nil {
		return packagedEndpointPersistenceFixture{}, fmt.Errorf("open packaged verification session: %w", err)
	}
	turn, err := instance.Core().TranscriptStore.BeginTurnContext(ctx, opened.ConversationID, packagedVerificationUserMessage)
	if err != nil {
		return packagedEndpointPersistenceFixture{}, fmt.Errorf("begin packaged verification turn: %w", err)
	}
	if _, err := instance.Core().TranscriptStore.CompleteTurnContext(
		ctx, opened.ConversationID, turn.ID, packagedVerificationAssistantReply,
	); err != nil {
		return packagedEndpointPersistenceFixture{}, fmt.Errorf("complete packaged verification turn: %w", err)
	}
	memory, err := management.CreateMemory(MemoryWrite{
		Kind:                  "preference",
		Scope:                 personal.Scope{Type: "global"},
		Content:               packagedVerificationMemoryContent,
		ConfidenceBasisPoints: 8000,
	})
	if err != nil {
		return packagedEndpointPersistenceFixture{}, fmt.Errorf("write packaged verification memory: %w", err)
	}
	fact, err := instance.Core().KnowledgeStore.InsertVerifiedKnowledgeContext(
		ctx,
		packagedVerificationKnowledgeTopic,
		packagedVerificationKnowledgeFact,
		opened.ConversationID,
		turn.ID,
		9000,
		nil,
	)
	if err != nil {
		return packagedEndpointPersistenceFixture{}, fmt.Errorf("write packaged verification knowledge: %w", err)
	}
	return packagedEndpointPersistenceFixture{
		characterID:    created.CharacterID,
		conversationID: opened.ConversationID,
		turnID:         turn.ID,
		memoryID:       memory.ID,
		knowledgeID:    fact.ID,
	}, nil
}

func verifyPackagedEndpointPersistence(ctx context.Context, instance *Runtime, fixture packagedEndpointPersistenceFixture) error {
	management := instance.Management()
	if management == nil || instance.Facade() == nil || instance.Core() == nil {
		return errors.New("reopened packaged endpoint persistence composition is incomplete")
	}
	catalog, err := management.Characters()
	if err != nil {
		return fmt.Errorf("read reopened packaged character catalog: %w", err)
	}
	if catalog.Active == nil || catalog.Active.CharacterID != fixture.characterID {
		return errors.New("reopened packaged endpoint did not restore the active character")
	}
	profile, err := management.Profile()
	if err != nil {
		return fmt.Errorf("read reopened packaged profile: %w", err)
	}
	if profile.PreferredName == nil || *profile.PreferredName != packagedVerificationPreferredName {
		return errors.New("reopened packaged endpoint did not restore the local profile")
	}
	opened, err := instance.Facade().OpenSession(ctx, session.OpenSessionRequest{
		Endpoint:    session.EndpointDesktop,
		EndpointKey: packagedVerificationEndpointKey,
		Interaction: session.Context{
			Audience:     session.AudienceSingle,
			Initiation:   session.InitiationDirect,
			Presentation: session.PresentationChat,
		},
	})
	if err != nil {
		return fmt.Errorf("reopen packaged verification session: %w", err)
	}
	if opened.ConversationID != fixture.conversationID {
		return errors.New("reopened packaged endpoint did not restore the conversation identity")
	}
	page, err := management.Conversation(ctx, fixture.conversationID, 0, 20)
	if err != nil {
		return fmt.Errorf("read reopened packaged conversation: %w", err)
	}
	if !packagedMessagePageContains(page, packagedVerificationUserMessage) ||
		!packagedMessagePageContains(page, packagedVerificationAssistantReply) {
		return errors.New("reopened packaged endpoint did not restore the completed Turn")
	}
	if _, err := management.TurnRuntime(ctx, fixture.conversationID, fixture.turnID); err != nil {
		return fmt.Errorf("read reopened packaged Turn runtime: %w", err)
	}
	memories, err := management.Memories(fixture.characterID)
	if err != nil {
		return fmt.Errorf("read reopened packaged memories: %w", err)
	}
	if !packagedMemoryCatalogContains(memories, fixture.memoryID) {
		return errors.New("reopened packaged endpoint did not restore personal memory")
	}
	knowledgeCatalog, err := management.Knowledge(ctx)
	if err != nil {
		return fmt.Errorf("read reopened packaged knowledge: %w", err)
	}
	if !packagedKnowledgeCatalogContains(knowledgeCatalog, fixture.knowledgeID) {
		return errors.New("reopened packaged endpoint did not restore verified knowledge")
	}
	hits, err := instance.Core().KnowledgeStore.SearchKnowledgeForIngestContext(
		ctx, packagedVerificationKnowledgeTopic, knowledge.MaxSearchCandidates,
	)
	if err != nil {
		return fmt.Errorf("recall reopened packaged knowledge: %w", err)
	}
	found := false
	for _, hit := range hits {
		if hit.ID == fixture.knowledgeID {
			found = true
			break
		}
	}
	if !found {
		return errors.New("reopened packaged endpoint did not recall verified knowledge")
	}
	return nil
}

func packagedMessagePageContains(page session.MessagePage, content string) bool {
	for _, message := range page.Messages {
		if message.Content == content {
			return true
		}
	}
	return false
}

func packagedMemoryCatalogContains(catalog personal.Catalog, memoryID string) bool {
	for _, record := range catalog.Global {
		if record.ID == memoryID {
			return true
		}
	}
	return false
}

func packagedKnowledgeCatalogContains(catalog knowledge.Catalog, knowledgeID string) bool {
	for _, record := range catalog.Verified {
		if record.ID == knowledgeID {
			return true
		}
	}
	return false
}

func verifyPackagedEndpointRuntimeState(ctx context.Context, instance *Runtime, verificationRoot string) error {
	if instance == nil || instance.Core() == nil || instance.Session() == nil || instance.Facade() == nil {
		return errors.New("packaged endpoint runtime composition is incomplete")
	}
	if instance.Core().RuntimeProfile != ProfileEndpointStrict {
		return fmt.Errorf("packaged runtime profile is %q, expected %q", instance.Core().RuntimeProfile, ProfileEndpointStrict)
	}
	management := instance.Management()
	if management == nil {
		return ErrManagementUnavailable
	}
	overview, err := management.Overview(ctx)
	if err != nil {
		return fmt.Errorf("read packaged endpoint overview: %w", err)
	}
	if overview.ConfigRoot != verificationRoot || overview.Profile != string(ProfileEndpointStrict) {
		return errors.New("packaged endpoint overview does not use its private strict-profile root")
	}
	if !overview.Storage.Ready || overview.Storage.Storage != "seekdb" || !overview.SecretKey.Ready {
		return errors.New("packaged endpoint local storage or encrypted SecretStore is not ready")
	}
	foundationStatus, err := instance.Core().Foundation.Status(ctx)
	if err != nil {
		return fmt.Errorf("read packaged endpoint schema: %w", err)
	}
	if foundationStatus.Schema.State != seekdb.SchemaCurrent {
		return fmt.Errorf("packaged endpoint schema state is %q, expected %q", foundationStatus.Schema.State, seekdb.SchemaCurrent)
	}
	if overview.Model.Configured || overview.Model.Ready || overview.Model.CredentialConfigured {
		return errors.New("fresh packaged endpoint unexpectedly configured a chat provider")
	}
	if overview.Semantic.Configured || overview.Semantic.Enabled || overview.Semantic.CredentialConfigured ||
		overview.Semantic.Provider != config.SemanticEmbeddingProviderNone || overview.Semantic.Dimensions != config.SemanticEmbeddingDimensions {
		return errors.New("fresh packaged endpoint unexpectedly configured an embedding provider")
	}
	if overview.WebSearch.Ready || overview.WebSearch.BaseURL != "" || instance.Core().WebSearch != nil {
		return errors.New("fresh packaged endpoint unexpectedly configured OpenSERP")
	}
	events, err := instance.Core().Model.ExecuteRequestContext(ctx, model.CompiledPromptRequest{
		Shape: model.ModelRequestShape{
			Lane:            model.PromptLaneRespond,
			Model:           "package-verification",
			Instructions:    "respond",
			MaxOutputTokens: 16,
		},
		Input: []model.PromptItem{{Type: model.PromptItemUserMessage, Content: "offline verification"}},
	})
	if err == nil || len(events) != 0 || !strings.Contains(strings.ToLower(err.Error()), "not configured") {
		return errors.New("fresh packaged endpoint did not fail the unconfigured chat provider explicitly")
	}
	if _, err := management.Profile(); err != nil {
		return fmt.Errorf("read packaged local profile: %w", err)
	}
	if _, err := management.Characters(); err != nil {
		return fmt.Errorf("read packaged local characters: %w", err)
	}
	if _, err := management.Intelligence(ctx); err != nil {
		return fmt.Errorf("read packaged local intelligence: %w", err)
	}
	if _, err := management.Knowledge(ctx); err != nil {
		return fmt.Errorf("read packaged local knowledge: %w", err)
	}
	plugins, err := management.Plugins()
	if err != nil {
		return fmt.Errorf("read packaged local plugin host: %w", err)
	}
	if !plugins.Ready || len(plugins.Instances) != 0 || len(plugins.Upgrades) != 0 {
		return errors.New("fresh packaged endpoint plugin host is not empty and deny-by-default")
	}
	return nil
}

func validateRuntimeVerificationRoot(root string) error {
	if root == "" || root != strings.TrimSpace(root) || strings.ContainsRune(root, 0) {
		return errors.New("packaged SeekDB verification root must be a clean non-empty path")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Dir(root) == root {
		return errors.New("packaged SeekDB verification root must be an absolute clean non-root path")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect packaged SeekDB verification root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("packaged SeekDB verification root must be a non-symlink directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("packaged SeekDB verification root permissions %04o are wider than 0700", info.Mode().Perm())
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read packaged SeekDB verification root: %w", err)
	}
	if len(entries) != 0 {
		return errors.New("packaged SeekDB verification root must be empty")
	}
	return nil
}
