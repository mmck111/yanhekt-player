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
