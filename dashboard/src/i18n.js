// Interface language. The dashboard follows the selected profile's
// language, so a Romanian-speaking person's carer sees it in Romanian.
// Texts are keyed by their English wording; missing entries stay English.

let current = 'en';

export function setLanguage(language) {
  current = language === 'ro' ? 'ro' : 'en';
  document.documentElement.lang = current;
}

export const language = () => current;

// t('Hello {name}', { name }) translates and fills in {placeholders}.
export function t(text, vars) {
  let out = current === 'ro' ? ro[text] ?? text : text;
  if (vars) for (const [key, value] of Object.entries(vars)) out = out.replaceAll('{' + key + '}', value);
  return out;
}

export const languages = { en: 'English', ro: 'Română' };

const ro = {
  // navigation and shell
  'Live': 'În direct', 'Patterns': 'Gesturi', 'Recordings': 'Înregistrări', 'Profiles': 'Profiluri', 'Phrases': 'Fraze',
  'Suggestions': 'Sugestii', 'Skip to content': 'Sari la conținut', 'Server access': 'Acces la server',
  'Admin token': 'Cheie de administrator', 'Save token': 'Salvează cheia', 'Dismiss': 'Închide', 'Connected': 'Conectat',
  'Connecting': 'Se conectează', 'Disconnected. Retrying…': 'Deconectat. Se reîncearcă…',
  // live page
  'Profile': 'Profil', 'No profiles yet': 'Încă nu există profiluri', 'Manage devices': 'Gestionează dispozitivele',
  '{n} new device(s) waiting to be assigned.': '{n} dispozitiv(e) nou(i) așteaptă să fie atribuite.',
  'Assign them on Profiles': 'Atribuie-le în Profiluri', 'Communication': 'Comunicare', 'Paused': 'În pauză', 'Text': 'Text',
  'Navigation': 'Navigare', 'a text field': 'un câmp de text',
  'Typing into {field}: the message is typed there, not spoken.': 'Se scrie în {field}: mesajul este scris acolo, nu rostit.',
  'No message composed': 'Niciun mesaj scris', 'Predictions': 'Predicții', 'Morse input': 'Intrare Morse',
  'Joystick keyboard': 'Tastatura joystickului', 'Switch to text': 'Treci la text', 'Switch to navigation': 'Treci la navigare',
  'Resume': 'Continuă', 'Pause': 'Pauză', 'Suggested correction': 'Corectură sugerată',
  'Suggested gesture match': 'Potrivire de gest sugerată', 'Flick': 'Mișcă scurt', 'to accept or': 'pentru a accepta sau',
  'to reject.': 'pentru a respinge.', 'Accept': 'Acceptă', 'Reject': 'Respinge', 'Gesture practice': 'Exersarea gesturilor',
  'switches modes.': 'schimbă modul.',
  'activates the current navigation target or selects the highlighted grid cell.': 'activează ținta curentă sau alege căsuța evidențiată din grilă.',
  'In Corner alphabet mode, flick': 'În alfabetul de colțuri, mișcă scurt', 'to speak as typed. Use': 'pentru a rosti textul așa cum e scris. Folosește',
  'to request a correction.': 'pentru a cere o corectură.',
  'For one letter, trace the corners continuously or start each flick within {ms} ms of the last release. Pause at center between letters.':
    'Pentru o literă, parcurge colțurile fără oprire sau începe fiecare mișcare în cel mult {ms} ms de la eliberare. Oprește-te în centru între litere.',
  'Stop recording': 'Oprește înregistrarea', 'Record gesture trace': 'Înregistrează traseul gesturilor',
  'Download trace CSV': 'Descarcă traseul (CSV)', 'Recording · {n} samples': 'Se înregistrează · {n} eșantioane',
  'Show the on-screen test joystick': 'Arată joystickul de test de pe ecran', 'Activity': 'Activitate',
  'No activity yet.': 'Încă nicio activitate.', 'Recording saved.': 'Înregistrare salvată.', 'Recording started.': 'Înregistrare pornită.',
  'Joystick practice': 'Exersare cu joystickul', 'On-screen test joystick': 'Joystick de test pe ecran', 'Continuous': 'Continuu',
  'Continuous up-left to up-right': 'Continuu din stânga sus în dreapta sus',
  // devices
  'Devices of this profile': 'Dispozitivele acestui profil', 'Joysticks': 'Joystickuri', 'Android': 'Android',
  'None yet.': 'Încă niciunul.', 'Add one': 'Adaugă unul', 'acts': 'acționează', 'online': 'conectat', 'offline': 'deconectat',
  'Joystick': 'Joystick', "{name}'s devices": 'Dispozitivele lui {name}', 'Name of {id}': 'Numele lui {id}',
  'Acts on the screen': 'Acționează pe ecran', 'Remove': 'Elimină',
  'No joystick yet. Turn one on, then add it below.': 'Încă niciun joystick. Pornește unul, apoi adaugă-l mai jos.',
  'No phone yet. Open the ActSpeed app on the phone and pick this profile, or add it below.':
    'Încă niciun telefon. Deschide aplicația ActSpeed pe telefon și alege acest profil, sau adaugă-l mai jos.',
  'New devices': 'Dispozitive noi',
  'None waiting. A joystick shows up here when it first connects; move it to tell which one it is.':
    'Niciunul nu așteaptă. Un joystick apare aici când se conectează prima dată; mișcă-l ca să vezi care este.',
  'moving now': 'se mișcă acum', 'Add to {name}': 'Adaugă la {name}', 'Forget': 'Uită', 'Devices of other profiles ({n})': 'Dispozitivele altor profiluri ({n})',
  'Move to {name}': 'Mută la {name}', 'Devices': 'Dispozitive',
  'Save the profile first, then add its joysticks and Android phones.': 'Salvează mai întâi profilul, apoi adaugă joystickurile și telefoanele Android.',
  // writing help
  'Next letter': 'Litera următoare', 'Next word': 'Cuvântul următor', 'Words': 'Cuvinte', 'space': 'spațiu', 'delete': 'șterge',
  'type it': 'scrie-l', 'speak': 'rostește', 'fix, then speak': 'corectează, apoi rostește', 'suggested by Claude': 'sugerat de Claude',
  'Suggested by Claude': 'Sugerat de Claude',
  // activity
  'Gesture': 'Gest', 'raw': 'brut', 'Spoken': 'Rostit', 'Unmatched': 'Nerecunoscut',
  // profiles page
  'New profile': 'Profil nou', 'Joystick settings': 'Setările joystickului', 'Name': 'Nume', 'Language': 'Limbă',
  'Navigation style': 'Stil de navigare', 'Gesture navigation': 'Navigare prin gesturi', 'Analog pointer': 'Indicator analogic',
  'Text input': 'Introducere text', 'Corner alphabet': 'Alfabetul de colțuri', 'Two-direction Morse': 'Morse în două direcții',
  'Letter grid': 'Grilă de litere', 'Activation radius': 'Raza de activare', 'Release radius': 'Raza de eliberare',
  'Direction dwell (ms)': 'Menținere direcție (ms)', 'Neutral settle (ms)': 'Revenire în centru (ms)', 'Hold threshold (ms)': 'Prag de menținere (ms)',
  'Center X': 'Centru X', 'Center Y': 'Centru Y', 'Compound gesture gap (ms)': 'Pauză între gesturi compuse (ms)',
  'Corner letter gap (ms)': 'Pauză între colțurile unei litere (ms)', 'Grid dwell selection (ms)': 'Alegere prin menținere în grilă (ms)',
  'Invert X': 'Inversează X', 'Invert Y': 'Inversează Y', 'Navigation diagonals': 'Diagonale în navigare', 'Enable Claude': 'Activează Claude',
  'Save profile': 'Salvează profilul',
  'What the person writes and hears, and the language of this dashboard and the phone while this profile is selected.':
    'Ce scrie și aude persoana, și limba acestui panou și a telefonului cât timp profilul este selectat.',
  // patterns
  'Gesture sequence': 'Secvența gestului', 'Mode': 'Mod', 'Output kind': 'Tip de rezultat', 'Output value': 'Valoare',
  'Scope': 'Pentru', 'All profiles': 'Toate profilurile', 'Enabled': 'Activ', 'Disabled': 'Inactiv', 'Save pattern': 'Salvează gestul',
  'Cancel': 'Anulează', 'Edit {gesture}': 'Editează {gesture}', 'Add a pattern': 'Adaugă un gest', 'Gesture library': 'Biblioteca de gesturi',
  'Filter by mode': 'Filtrează după mod', 'All modes': 'Toate modurile', 'Edit': 'Editează', 'Disable': 'Dezactivează', 'Enable': 'Activează',
  'Action': 'Acțiune', 'Character': 'Caracter', 'Phrase': 'Frază', 'Control': 'Comandă',
  // phrases
  'Phrase bank': 'Banca de fraze', 'New phrase': 'Frază nouă', 'Add phrase': 'Adaugă fraza', 'Remove phrase: {text}': 'Elimină fraza: {text}',
  'Shared phrases in {language}, plus every profile\'s own.': 'Fraze comune în {language}, plus cele proprii fiecărui profil.',
  // suggestions
  'AI suggestions': 'Sugestii AI', 'Refresh suggestions': 'Reîmprospătează sugestiile', 'No suggestions yet.': 'Încă nicio sugestie.',
  'Save as pattern': 'Salvează ca gest', 'pending': 'în așteptare', 'accepted': 'acceptată', 'rejected': 'respinsă',
  'auto applied': 'aplicată automat', 'expired': 'expirată', 'edited': 'editată',
  // recordings
  'Refresh recordings': 'Reîmprospătează înregistrările', 'Start a recording on the Live page.': 'Pornește o înregistrare pe pagina În direct.',
  'Map recorded gesture': 'Atribuie gestul înregistrat', 'recording': 'se înregistrează', 'done': 'gata',
  // gestures
  'up': 'sus', 'up-right': 'dreapta sus', 'right': 'dreapta', 'down-right': 'dreapta jos', 'down': 'jos', 'down-left': 'stânga jos',
  'left': 'stânga', 'up-left': 'stânga sus', 'center': 'centru', 'clockwise circle': 'cerc în sensul acelor de ceasornic',
  'counterclockwise circle': 'cerc în sens invers acelor de ceasornic', 'held': 'menținut', ', then ': ', apoi ',
  // event types
  'mode changed': 'mod schimbat', 'text update': 'text actualizat', 'suggestion': 'sugestie', 'suggestion resolved': 'sugestie rezolvată',
  'suggestion applied': 'sugestie aplicată', 'assist': 'citire de ecran', 'assist thinking': 'se citește ecranul',
  'assist failed': 'citirea a eșuat', 'assist resolved': 'citire închisă', 'assist request': 'cerere de citire', 'memory': 'memorie',
  'commit text': 'text scris', 'field': 'câmp', 'alert': 'alertă', 'correcting': 'se corectează', 'devices changed': 'dispozitive schimbate',
  // action catalogue
  'Move pointer': 'Mută indicatorul', 'Left click': 'Clic stânga', 'Double click': 'Dublu clic', 'Right click': 'Clic dreapta', 'Toggle drag lock': 'Blochează / eliberează tragerea', 'Scroll': 'Derulează', 'Arrow up': 'Săgeată sus', 'Arrow down': 'Săgeată jos', 'Arrow left': 'Săgeată stânga', 'Arrow right': 'Săgeată dreapta', 'Enter': 'Enter', 'Escape': 'Escape', 'Tab': 'Tab', 'Backspace': 'Șterge înapoi', 'Copy': 'Copiază', 'Paste': 'Lipește', 'Undo': 'Anulează acțiunea', 'Switch window': 'Schimbă fereastra', 'Home': 'Acasă', 'Back': 'Înapoi', 'App switcher': 'Aplicații recente', 'Volume up': 'Volum mai tare', 'Volume down': 'Volum mai încet', 'Mute': 'Fără sunet', 'Play / pause': 'Redă / pauză', 'Next track': 'Piesa următoare', 'Previous track': 'Piesa anterioară', 'Tap on screen': 'Atinge ecranul', 'Long press on screen': 'Apasă lung pe ecran', 'Type text into a field': 'Scrie text într-un câmp', 'Scroll the screen': 'Derulează ecranul', 'Scan: next item': 'Scanare: elementul următor', 'Scan: previous item': 'Scanare: elementul anterior', 'Scan: select item': 'Scanare: alege elementul', 'profile changed': 'profil schimbat',
  'timing learned': 'ritm învățat', 'alert sent': 'alertă trimisă', 'Screen read': 'Ecran citit', 'Remembered': 'Memorat',
};
