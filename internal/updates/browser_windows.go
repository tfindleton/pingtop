package updates

import "golang.org/x/sys/windows"

func launchBrowser(targetURL string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(targetURL)
	if err != nil {
		return err
	}
	// ShellExecute loads shell32 from the system directory and avoids looking
	// up rundll32.exe or url.dll in an attacker-controlled working directory.
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}
