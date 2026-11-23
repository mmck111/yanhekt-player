#ifndef AppVersion
  #define AppVersion "2.1.0"
#endif

[Setup]
AppId={{8E9A7D6B-5D4F-4E3A-9C1B-2E4D6F8A0C12}
AppName=Yanhekt Player
AppVersion={#AppVersion}
AppVerName=Yanhekt Player {#AppVersion}
AppPublisher=Yanhekt Player
DefaultDirName={autopf}\Yanhekt Player
DisableDirPage=no
DefaultGroupName=Yanhekt Player
AllowNoIcons=yes
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
SetupIconFile=..\android\icon\icon.ico
UninstallDisplayIcon={app}\yanhekt.exe
OutputDir=..\dist
OutputBaseFilename=yanhekt-setup-{#AppVersion}-windows-amd64
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
CloseApplications=yes
RestartApplications=no

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut for Yanhekt Player"; GroupDescription: "Additional shortcuts:"

[Files]
Source: "..\dist\yanhekt.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\README.md"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\Yanhekt Player"; Filename: "{app}\yanhekt.exe"
Name: "{group}\Uninstall Yanhekt Player"; Filename: "{uninstallexe}"
Name: "{autodesktop}\Yanhekt Player"; Filename: "{app}\yanhekt.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\yanhekt.exe"; Description: "Launch Yanhekt Player"; Flags: nowait postinstall skipifsilent

[Code]
var
  DownloadDirPage: TInputDirWizardPage;

function DefaultVideoDir: string;
var
  RegDir: string;
begin
  if RegQueryStringValue(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Explorer\Shell Folders', '{374DE290-123F-4565-9164-39C4925E467B}', RegDir) and (RegDir <> '') then
  begin
    Result := RegDir;
    Exit;
  end;
  Result := ExpandConstant('{userdocs}\Yanhekt Downloads');
end;

function JsonEscape(const S: string): string;
begin
  Result := S;
  StringChangeEx(Result, '\', '\\', True);
  StringChangeEx(Result, '"', '\"', True);
end;

procedure InitializeWizard;
begin
  DownloadDirPage := CreateInputDirPage(
    wpSelectDir,
    '默认视频下载位置',
    '请选择课程视频的默认保存文件夹',
    '请选择一个你有写入权限的文件夹作为默认视频下载目录。' + #13#10 +
    '注：安装完成后，随时可在播放器左侧「⚙ 设置」中修改默认下载路径及其他播放偏好。',
    False,
    'Yanhekt Downloads'
  );
  DownloadDirPage.Add('默认下载目录：');
  DownloadDirPage.Values[0] := DefaultVideoDir;
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  DlDir, JsonText, LocalDir: string;
begin
  if CurStep = ssPostInstall then
  begin
    DlDir := Trim(DownloadDirPage.Values[0]);
    if DlDir <> '' then
    begin
      ForceDirectories(DlDir);
      JsonText := '{' + #13#10 +
        '  "downloadDir": "' + JsonEscape(DlDir) + '",' + #13#10 +
        '  "askSavePath": false' + #13#10 +
        '}' + #13#10;
      SaveStringToFile(ExpandConstant('{app}\defaults.json'), UTF8Encode(JsonText), False);
      LocalDir := ExpandConstant('{localappdata}\yanhekt');
      ForceDirectories(LocalDir);
      if not FileExists(LocalDir + '\settings.json') then
        SaveStringToFile(LocalDir + '\settings.json', UTF8Encode(JsonText), False);
    end;
  end;
end;