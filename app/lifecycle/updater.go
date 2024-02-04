package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/jmorganca/ollama/app/store"
	"github.com/jmorganca/ollama/version"
)

var (
	UpdateCheckURLBase = "https://ollama.ai/api/update"
	UpdateDownloaded   = false
)

func GetUpdateCheckURL(id string) string {
	return UpdateCheckURLBase + "?os=" + runtime.GOOS + "&arch=" + runtime.GOARCH + "&version=" + version.Version + "&id=" + id
}

// TODO - maybe move up to the API package?
type UpdateResponse struct {
	UpdateURL     string `json:"url"`
	UpdateVersion string `json:"version"`
}

func IsNewReleaseAvailable() (bool, UpdateResponse) {
	var updateResp UpdateResponse
	updateCheckURL := GetUpdateCheckURL(store.GetID())
	slog.Debug(fmt.Sprintf("XXX checking for update via %s", updateCheckURL))
	resp, err := http.Get(updateCheckURL)
	if err != nil {
		slog.Debug(fmt.Sprintf("XXX error checking for update: %s", err))
		return false, updateResp
	}
	defer resp.Body.Close()
	if resp.StatusCode == 204 {
		slog.Debug("XXX got 204 when checking for update")
		return false, updateResp
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Debug(fmt.Sprintf("XXX failed to read body response: %s", err))
	}
	err = json.Unmarshal(body, &updateResp)
	if err != nil {
		slog.Warn(fmt.Sprintf("malformed response checking for update: %s", err))
		return false, updateResp
	}
	slog.Info("New update available at" + updateResp.UpdateURL)
	return true, updateResp
}

func DownloadNewRelease(updateResp UpdateResponse) error {
	updateURL, err := url.Parse(updateResp.UpdateURL)
	if err != nil {
		return fmt.Errorf("failed to parse update URL %s: %w", updateResp.UpdateURL, err)
	}
	escapedFilename := filepath.Join(UpdateStageDir, url.PathEscape(updateURL.Path))
	_, err = os.Stat(UpdateStageDir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(UpdateStageDir, 0o755); err != nil {
			return fmt.Errorf("create ollama dir %s: %v", UpdateStageDir, err)
		}
	}
	_, err = os.Stat(escapedFilename)
	if errors.Is(err, os.ErrNotExist) {
		slog.Debug(fmt.Sprintf("XXX downloading %s", updateResp.UpdateURL))
		resp, err := http.Get(updateResp.UpdateURL)
		if err != nil {
			return fmt.Errorf("error downloading update: %w", err)
		}
		defer resp.Body.Close()
		payload, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read body response: %w", err)
		}
		fp, err := os.OpenFile(escapedFilename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return fmt.Errorf("write payload %s: %w", escapedFilename, err)
		}
		defer fp.Close()
		if n, err := fp.Write(payload); err != nil || n != len(payload) {
			return fmt.Errorf("write payload %s: %d vs %d -- %w", escapedFilename, n, len(payload), err)
		}
		slog.Debug(fmt.Sprintf("XXX completed writing out update payload to %s", escapedFilename))
	} else if err != nil {
		return fmt.Errorf("XXX unexpected stat error %w", err)
	} else {
		slog.Debug("XXX update already downloaded")
	}
	UpdateDownloaded = true
	return nil
}

func StartBackgroundUpdaterChecker(ctx context.Context, cb func(string) error) {
	go func() {
		// TODO - remove this - only for debugging...
		time.Sleep(5 * time.Second)

		for {
			available, resp := IsNewReleaseAvailable()
			if available {
				err := DownloadNewRelease(resp)
				if err != nil {
					slog.Error(fmt.Sprintf("failed to download new release: %s", err))
				}
				err = cb("TODO version")
				if err != nil {
					slog.Debug("XXX failed to register update available with tray")
				}
			}
			select {
			case <-ctx.Done():
				slog.Debug("XXX stopping background update checker")
				return
			default:
				time.Sleep(60 * 60 * time.Second)
			}
		}
	}()
}

func DoUpgrade() error {
	installerExe := filepath.Join(UpdateStageDir, Installer)

	_, err := os.Stat(installerExe)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("could not locate ollama installer at %s", installerExe)
	}
	slog.Debug(fmt.Sprintf("XXX attempting to start installer %s", installerExe))

	installArgs := []string{
		"/SP", // Skip the "This will install... Do you wish to continue" prompt
		"/SILENT",
		// "/VERYSILENT", // TODO - use this one once it's validated
		"/SUPPRESSMSGBOXES",  // Might not be needed?
		"/CLOSEAPPLICATIONS", // Quit the tray app if it's still running
		// "/FORCECLOSEAPPLICATIONS", // Force close the tray app - might be needed
	}
	cmd := exec.Command(installerExe, installArgs...)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("unable to start ollama app %w", err)
	}

	if cmd.Process != nil {
		err = cmd.Process.Release()
		if err != nil {
			slog.Error(fmt.Sprintf("failed to release server process: %s", err))
		}
	} else {
		// TODO - some details about why it didn't start, or is this a pedantic error case?
		return fmt.Errorf("Installer process did not start")
	}
	slog.Info("Installer started in background, exiting")

	os.Exit(0)
	// Not reached
	return nil
}
