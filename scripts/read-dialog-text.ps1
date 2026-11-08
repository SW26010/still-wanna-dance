param([Parameter(Mandatory)][int]$ProcessId, [switch]$CheckBounds)
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
# Read only the isolated child process, never another application's windows.
$condition = [Windows.Automation.PropertyCondition]::new([Windows.Automation.AutomationElement]::ProcessIdProperty, $ProcessId)
$windows = [Windows.Automation.AutomationElement]::RootElement.FindAll([Windows.Automation.TreeScope]::Children, $condition)
foreach ($window in $windows) {
    $bounds = $window.Current.BoundingRectangle
    foreach ($element in $window.FindAll([Windows.Automation.TreeScope]::Subtree, [Windows.Automation.Condition]::TrueCondition)) {
        if ($CheckBounds -and ($element.Current.ControlType -eq [Windows.Automation.ControlType]::Text -or $element.Current.ControlType -eq [Windows.Automation.ControlType]::Button)) {
            $rect = $element.Current.BoundingRectangle
            if ($element.Current.IsOffscreen -or $rect.IsEmpty -or $rect.Width -le 0 -or $rect.Height -le 0 -or -not $bounds.Contains($rect)) {
                throw "Dialog content is offscreen or outside the window: $($element.Current.Name)"
            }
        }
        $element.Current.Name
    }
}
